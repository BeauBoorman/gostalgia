package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"gostalgia/internal/events"
	"gostalgia/internal/security"
)

type auditSecret struct {
	Token    string `json:"token"`
	Password string `json:"password"`
	Content  string `json:"content"`
	Error    string `json:"error"`
}

func (auditSecret) Type() string { return "test.secret" }

func eventServer(t *testing.T) (*Server, *events.Bus, *Router) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	r, bus := NewRouter(), events.NewBus()
	must(t, RegisterEvents(r, bus))
	must(t, r.Handle("test/echo", func(ctx context.Context, req Request) (any, error) {
		var p int
		if err := DecodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return p, nil
	}))
	s := NewServer(ln, r, "sekrit", nil)
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return s, bus, r
}

func eventClient(t *testing.T, s *Server) *Client {
	t.Helper()
	conn, err := net.Dial("tcp", s.ln.Addr().String())
	must(t, err)
	c, err := NewClient(conn, "sekrit")
	must(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitSubscriberCount(t *testing.T, bus *events.Bus, want int) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if bus.SubscriberCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("subscribers = %d, want %d", bus.SubscriberCount(), want)
}

func nextEvent(t *testing.T, ctx context.Context, sub *ClientSubscription) Notification {
	t.Helper()
	select {
	case n, ok := <-sub.Events:
		if !ok {
			t.Fatal("subscription closed")
		}
		return n
	case <-ctx.Done():
		t.Fatal("timed out waiting for an event")
		return Notification{}
	}
}

func TestSimultaneousSlowCallFastCallAndEvent(t *testing.T) {
	s, bus, r := eventServer(t)
	c, ctx := eventClient(t, s), testContext(t)
	sub, err := c.Subscribe(ctx, SubscribeParams{Topic: "*"})
	must(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	must(t, r.Handle("test/slow", func(ctx context.Context, req Request) (any, error) {
		close(entered)
		select {
		case <-release:
			return "released", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	slow := make(chan error, 1)
	go func() { slow <- c.Call(ctx, "test/slow", nil, nil) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("slow call never entered")
	}
	var out int
	must(t, c.Call(ctx, "test/echo", 42, &out))
	if out != 42 {
		t.Fatalf("mismatched response: %d", out)
	}
	bus.Publish("tester", auditSecret{Content: "never send this"})
	n := nextEvent(t, ctx, sub)
	if n.Event.ID != 1 || n.Version != 1 || n.Subscription != sub.Info.Subscription {
		t.Fatalf("notification: %+v", n)
	}
	close(release)
	must(t, <-slow)
	must(t, sub.Close(ctx))
	waitSubscriberCount(t, bus, 0)
	if _, ok := <-sub.Events; ok {
		t.Fatal("event channel not closed by unsubscribe")
	}
}

func TestConcurrentCallsAndFloodWithSlowConsumer(t *testing.T) {
	s, bus, _ := eventServer(t)
	c, ctx := eventClient(t, s), testContext(t)
	sub, err := c.Subscribe(ctx, SubscribeParams{Topic: "*", Buffer: 4})
	must(t, err)
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				want, got := i*100+j, 0
				if err := c.Call(ctx, "test/echo", want, &got); err != nil {
					failures <- err
					return
				}
				if got != want {
					failures <- fmt.Errorf("got %d, want %d", got, want)
					return
				}
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				bus.Publish("tester", auditSecret{})
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	var previous uint64
	for {
		n := nextEvent(t, ctx, sub)
		if n.Event.ID <= previous {
			t.Fatal("events not ordered by cursor")
		}
		previous = n.Event.ID
		if n.Event.ID == 4000 {
			if n.Dropped == 0 {
				t.Fatal("slow consumer overflow not reported")
			}
			break
		}
	}
	history, err := bus.Recent("*", 0, 0)
	must(t, err)
	if len(history.Events) != events.HistoryLimit || !history.Resync {
		t.Fatal("history retention not bounded")
	}
	must(t, sub.Close(ctx))
}

func TestDisconnectReplayAndRestartResync(t *testing.T) {
	s, bus, _ := eventServer(t)
	c, ctx := eventClient(t, s), testContext(t)
	sub, err := c.Subscribe(ctx, SubscribeParams{Topic: "test.secret", Buffer: 16})
	must(t, err)
	bus.Publish("tester", auditSecret{})
	last := nextEvent(t, ctx, sub).Event.ID
	epoch := sub.Info.Epoch
	must(t, c.Close())
	waitSubscriberCount(t, bus, 0)
	for i := 0; i < 5; i++ {
		bus.Publish("tester", auditSecret{})
	}
	c = eventClient(t, s)
	sub, err = c.Subscribe(ctx, SubscribeParams{Topic: "test.secret", Buffer: 16, After: &last, Epoch: epoch})
	must(t, err)
	if sub.Info.Resync || sub.Info.Cursor != 6 {
		t.Fatalf("replay acknowledgement: %+v", sub.Info)
	}
	for id := uint64(2); id <= 6; id++ {
		if n := nextEvent(t, ctx, sub); n.Event.ID != id {
			t.Fatalf("replay event: %+v, want %d", n, id)
		}
	}
	must(t, sub.Close(ctx))
	// A restarted runtime can have equal or higher IDs. Epoch, not ID
	// comparisons alone, must detect that cursor discontinuity.
	other, _, _ := eventServer(t)
	restarted := eventClient(t, other)
	sub, err = restarted.Subscribe(ctx, SubscribeParams{Topic: "*", After: &last, Epoch: epoch})
	must(t, err)
	if !sub.Info.Resync || sub.Info.Epoch == epoch {
		t.Fatalf("restart not reported: %+v", sub.Info)
	}
	must(t, sub.Close(ctx))
}

func TestReplayOverflowReportsResync(t *testing.T) {
	s, bus, _ := eventServer(t)
	for i := 0; i < 300; i++ {
		bus.Publish("tester", auditSecret{})
	}
	c, ctx := eventClient(t, s), testContext(t)
	after := uint64(0)
	sub, err := c.Subscribe(ctx, SubscribeParams{Topic: "*", Buffer: 2, After: &after, Epoch: bus.Epoch()})
	must(t, err)
	if !sub.Info.Resync || sub.Info.Cursor != 300 || sub.Info.Oldest != 45 {
		t.Fatalf("retention/replay overflow not reported: %+v", sub.Info)
	}
	if _, err := c.Subscribe(ctx, SubscribeParams{Topic: "*"}); err == nil {
		t.Fatal("accepted a second subscription on one connection")
	}
	n := nextEvent(t, ctx, sub)
	if n.Event.ID != 299 {
		t.Fatalf("drop-oldest replay: %+v", n)
	}
	n = nextEvent(t, ctx, sub)
	if n.Event.ID != 300 || n.Dropped != 254 {
		t.Fatalf("replay drop accounting: %+v", n)
	}
	must(t, sub.Close(ctx))
	if _, err := c.Subscribe(ctx, SubscribeParams{Topic: "*", After: &after}); err == nil {
		t.Fatal("accepted a resume cursor without an epoch")
	}
}

func TestHistoryAuthorizationRedactionAndFilters(t *testing.T) {
	s, bus, r := eventServer(t)
	c, ctx := eventClient(t, s), testContext(t)
	secrets := []string{"private-token", "password-value", "document-body", "credential in error"}
	bus.Publish("tester", auditSecret{secrets[0], secrets[1], secrets[2], secrets[3]})
	var history events.History
	must(t, c.Call(ctx, HistoryMethod, HistoryParams{Topic: "test.secret", Limit: 1}, &history))
	if len(history.Events) != 1 || history.Cursor != 1 || history.Epoch == "" {
		t.Fatalf("history = %+v", history)
	}
	raw, err := json.Marshal(history)
	must(t, err)
	for _, secret := range append(secrets, "sekrit") {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("history leaked %q", secret)
		}
	}
	must(t, c.Call(ctx, HistoryMethod, HistoryParams{Topic: "other"}, &history))
	if len(history.Events) != 0 {
		t.Fatal("history filter ignored")
	}
	for _, method := range []string{SubscribeMethod, UnsubscribeMethod, HistoryMethod} {
		for _, caps := range []*security.Capabilities{nil, security.NewCapabilities(security.CapIPC)} {
			resp := r.Dispatch(WithCapabilities(ctx, caps), Request{ID: 1, Method: method})
			if resp.OK || !strings.Contains(resp.Error, "permission denied") {
				t.Fatalf("unauthorized %s: %+v", method, resp)
			}
		}
	}
	for _, p := range []HistoryParams{
		{Topic: "*", Limit: -1}, {Topic: "*", Limit: events.HistoryLimit + 1},
		{Topic: "*", After: 1}, {Topic: "bad topic"},
	} {
		if err := c.Call(ctx, HistoryMethod, p, nil); err == nil {
			t.Fatalf("accepted invalid history params: %+v", p)
		}
	}
	if _, err := c.Subscribe(ctx, SubscribeParams{Topic: "*", Buffer: events.BufferLimit + 1}); err == nil {
		t.Fatal("accepted oversized subscription")
	}
	var reset events.History
	must(t, c.Call(ctx, HistoryMethod, HistoryParams{Topic: "*", After: 1, Epoch: "old-runtime"}, &reset))
	if !reset.Resync || len(reset.Events) != 1 {
		t.Fatalf("history epoch reset: %+v", reset)
	}
}

func rawPeer(t *testing.T, s *Server) (net.Conn, *bufio.Scanner, *json.Encoder) {
	t.Helper()
	conn, err := net.Dial("tcp", s.ln.Addr().String())
	must(t, err)
	t.Cleanup(func() { conn.Close() })
	must(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	scanner, encoder := bufio.NewScanner(conn), json.NewEncoder(conn)
	must(t, encoder.Encode(Request{ID: 1, Method: "auth", Params: json.RawMessage(`{"token":"sekrit"}`)}))
	if !scanner.Scan() {
		t.Fatal("no auth response")
	}
	var resp Response
	must(t, json.Unmarshal(scanner.Bytes(), &resp))
	if !resp.OK {
		t.Fatal("auth rejected")
	}
	return conn, scanner, encoder
}

func TestMalformedRequestsDisconnectAndCleanSubscriptions(t *testing.T) {
	frames := []string{
		"{\n", "null\n", "[]\n", "{}\n",
		`{"id":0,"method":"test/echo"}` + "\n",
		`{"id":3,"method":1}` + "\n",
		`{"id":3,"method":"test/echo"} {}` + "\n",
		strings.Repeat("x", maxLine+1) + "\n",
	}
	for i, frame := range frames {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, bus, _ := eventServer(t)
			conn, scanner, encoder := rawPeer(t, s)
			must(t, encoder.Encode(Request{ID: 2, Method: SubscribeMethod, Params: json.RawMessage(`{"topic":"*"}`)}))
			if !scanner.Scan() {
				t.Fatal("no subscribe response")
			}
			waitSubscriberCount(t, bus, 1)
			_, _ = conn.Write([]byte(frame))
			if scanner.Scan() {
				t.Fatalf("malformed request accepted: %s", scanner.Bytes())
			}
			waitSubscriberCount(t, bus, 0)
			healthy := eventClient(t, s)
			must(t, healthy.Call(testContext(t), "test/echo", 1, nil))
		})
	}
}

func TestRequestFloodAndDuplicateIDsBoundWork(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprint(duplicate), func(t *testing.T) {
			s, bus, r := eventServer(t)
			must(t, r.Handle("test/block", func(ctx context.Context, req Request) (any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}))
			conn, scanner, encoder := rawPeer(t, s)
			must(t, encoder.Encode(Request{ID: 2, Method: SubscribeMethod, Params: json.RawMessage(`{"topic":"*"}`)}))
			if !scanner.Scan() {
				t.Fatal("no subscribe acknowledgement")
			}
			for i := 0; i < maxInFlight+1; i++ {
				id := int64(i + 3)
				if duplicate {
					id = 3
				}
				if err := encoder.Encode(Request{ID: id, Method: "test/block"}); err != nil {
					break
				}
			}
			if scanner.Scan() {
				t.Fatal("flooded peer received a response")
			}
			conn.Close()
			waitSubscriberCount(t, bus, 0)
		})
	}
}

func TestMalformedResponsesFailPendingCalls(t *testing.T) {
	for i, frame := range []string{
		"{\n", "null\n", "{}\n", `{"id":2,"ok":"yes"}` + "\n",
		`{"kind":"event","version":2,"subscription":1,"event":{"id":1}}` + "\n",
		strings.Repeat("x", maxLine+1) + "\n",
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			clientConn, peer := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer peer.Close()
				scanner := bufio.NewScanner(peer)
				if !scanner.Scan() {
					return
				}
				_, _ = fmt.Fprintln(peer, `{"id":1,"ok":true}`)
				if !scanner.Scan() {
					return
				}
				_, _ = peer.Write([]byte(frame))
			}()
			c, err := NewClient(clientConn, "sekrit")
			must(t, err)
			ctx := testContext(t)
			if err := c.Call(ctx, "test/echo", 1, nil); err == nil {
				t.Fatal("malformed response accepted")
			}
			must(t, c.Close())
			<-done
		})
	}
}

func TestCancelOneCallKeepsConcurrentCallUsable(t *testing.T) {
	s, _, r := eventServer(t)
	c, ctx := eventClient(t, s), testContext(t)
	release, entered := make(chan struct{}), make(chan struct{})
	must(t, r.Handle("test/block", func(ctx context.Context, req Request) (any, error) {
		close(entered)
		select {
		case <-release:
			return 999, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	callCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- c.Call(callCtx, "test/block", nil, nil) }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled call = %v", err)
	}
	must(t, c.Call(ctx, "test/echo", 7, nil))
	close(release)
	for i := 0; i < 10; i++ {
		var out int
		must(t, c.Call(ctx, "test/echo", i, &out))
		if out != i {
			t.Fatalf("late response mixed into another call: %d", out)
		}
	}
}

func TestUnreadingSubscriberDoesNotBlockPublishersOrShutdown(t *testing.T) {
	s, bus, _ := eventServer(t)
	// net.Pipe has no socket send buffer: the first event write blocks,
	// making this a deterministic transport-backpressure test.
	conn, serverConn := net.Pipe()
	t.Cleanup(func() { conn.Close() })
	s.mu.Lock()
	s.conns[serverConn] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	go s.handleConn(serverConn)
	scanner, encoder := bufio.NewScanner(conn), json.NewEncoder(conn)
	must(t, encoder.Encode(Request{ID: 1, Method: "auth", Params: json.RawMessage(`{"token":"sekrit"}`)}))
	if !scanner.Scan() {
		t.Fatal("no auth response")
	}
	must(t, encoder.Encode(Request{ID: 2, Method: SubscribeMethod, Params: json.RawMessage(`{"topic":"*","buffer":1}`)}))
	if !scanner.Scan() {
		t.Fatal("no subscribe response")
	}
	// Stop reading this raw socket. Healthy command peers must still work.
	c, ctx := eventClient(t, s), testContext(t)
	published := make(chan struct{})
	go func() {
		defer close(published)
		for i := 0; i < 20000; i++ {
			bus.Publish("tester", auditSecret{})
		}
	}()
	for i := 0; i < 20; i++ {
		must(t, c.Call(ctx, "test/echo", i, nil))
	}
	select {
	case <-published:
	case <-ctx.Done():
		t.Fatal("subscriber blocked the publisher")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("subscriber blocked shutdown")
	}
	conn.Close()
	waitSubscriberCount(t, bus, 0)
}
