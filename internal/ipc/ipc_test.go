package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gostalgia/internal/security"
)

func TestRouterDispatch(t *testing.T) {
	r := NewRouter()
	must(t, r.Handle("math/add", func(ctx context.Context, req Request) (any, error) {
		var p struct {
			A, B int
		}
		if err := DecodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return map[string]int{"sum": p.A + p.B}, nil
	}))

	resp := r.Dispatch(context.Background(), Request{ID: 7, Method: "math/add", Params: json.RawMessage(`{"A":2,"B":3}`)})
	if !resp.OK || resp.ID != 7 {
		t.Fatalf("resp = %+v", resp)
	}
	var out map[string]int
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out["sum"] != 5 {
		t.Fatalf("sum = %d, want 5", out["sum"])
	}
}

func TestRouterUnknownMethod(t *testing.T) {
	r := NewRouter()
	resp := r.Dispatch(context.Background(), Request{ID: 1, Method: "ghost"})
	if resp.OK || resp.Error == "" {
		t.Fatalf("resp = %+v, want error", resp)
	}
}

func TestRouterHandlerErrorBecomesResponseError(t *testing.T) {
	r := NewRouter()
	must(t, r.Handle("x/fail", func(ctx context.Context, req Request) (any, error) {
		return nil, errors.New("expected failure")
	}))
	resp := r.Dispatch(context.Background(), Request{ID: 2, Method: "x/fail"})
	if resp.OK || resp.Error != "expected failure" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestRouterRecoversFromHandlerPanic(t *testing.T) {
	r := NewRouter()
	must(t, r.Handle("x/boom", func(ctx context.Context, req Request) (any, error) {
		panic("handler exploded")
	}))
	resp := r.Dispatch(context.Background(), Request{ID: 3, Method: "x/boom"})
	if resp.OK || resp.Error == "" {
		t.Fatalf("panic escaped Dispatch: %+v", resp)
	}
}

func TestRouterDuplicateRouteRejected(t *testing.T) {
	r := NewRouter()
	h := func(ctx context.Context, req Request) (any, error) { return nil, nil }
	if err := r.Handle("dup", h); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle("dup", h); err == nil {
		t.Fatal("duplicate route accepted, want error")
	}
}

func TestRouterUnhandlePrefix(t *testing.T) {
	r := NewRouter()
	h := func(ctx context.Context, req Request) (any, error) { return nil, nil }
	must(t, r.Handle("app/one/a", h))
	must(t, r.Handle("app/one/b", h))
	must(t, r.Handle("app/two/a", h))
	r.UnhandlePrefix("app/one")
	methods := r.Methods()
	if len(methods) != 1 || methods[0] != "app/two/a" {
		t.Fatalf("methods after unhandle = %v", methods)
	}
}

func TestRequireCap(t *testing.T) {
	ctx := WithCapabilities(context.Background(), security.NewCapabilities(security.CapFileRead))
	if err := RequireCap(ctx, security.CapFileRead); err != nil {
		t.Errorf("granted cap rejected: %v", err)
	}
	if err := RequireCap(ctx, security.CapShutdown); err == nil {
		t.Error("missing cap accepted")
	}
	if err := RequireCap(context.Background(), security.CapFileRead); err == nil {
		t.Error("cap check passed with no capability context")
	}
}

// serverClient builds a live server over a loopback TCP listener and
// returns a connected, authenticated client.
func serverClient(t *testing.T) (*Server, *Client) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRouter()
	must(t, r.Handle("test/whoami", func(ctx context.Context, req Request) (any, error) {
		caps := Capabilities(ctx)
		if caps == nil {
			return nil, errors.New("no capabilities in context")
		}
		return map[string]any{"admin": caps.Has(security.CapAdmin)}, nil
	}))
	log := slog.New(slog.NewTextHandler(discard{}, nil))
	srv := NewServer(ln, r, "sekrit", log)
	t.Cleanup(func() { srv.Close() })
	go srv.Serve()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(conn, "sekrit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return srv, client
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func TestClientRoundTrip(t *testing.T) {
	_, client := serverClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var out map[string]any
	if err := client.Call(ctx, "test/whoami", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out["admin"] != true {
		t.Fatalf("authenticated client should carry admin caps, got %v", out)
	}
}

func TestClientBadTokenRejected(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ln, NewRouter(), "sekrit", slog.New(slog.NewTextHandler(discard{}, nil)))
	defer srv.Close()
	go srv.Serve()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(conn, "wrong"); err == nil {
		t.Fatal("NewClient with wrong token succeeded, want auth failure")
	}
}

func TestClientNonAuthFirstRequestRejected(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ln, NewRouter(), "sekrit", slog.New(slog.NewTextHandler(discard{}, nil)))
	defer srv.Close()
	go srv.Serve()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	// Speak the protocol directly: first request is not auth.
	req := Request{ID: 1, Method: "sys/ping"}
	b, _ := json.Marshal(req)
	conn.Write(append(b, '\n'))
	buf := make([]byte, 512)
	n, _ := conn.Read(buf)
	var resp Response
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("bad response: %v (%q)", err, string(buf[:n]))
	}
	if resp.OK || resp.Error == "" {
		t.Fatalf("non-auth first request accepted: %+v", resp)
	}
}

func TestClientDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRouter()
	must(t, r.Handle("test/slow", func(ctx context.Context, req Request) (any, error) {
		time.Sleep(3 * time.Second)
		return "done", nil
	}))
	srv := NewServer(ln, r, "t", slog.New(slog.NewTextHandler(discard{}, nil)))
	defer srv.Close()
	go srv.Serve()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(conn, "t")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := client.Call(ctx, "test/slow", nil, nil); err == nil {
		t.Fatal("slow call succeeded despite deadline")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("deadline not honored: took %s", elapsed)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestServerCloseUnderConnectionBurst hammers Close against a server that
// is accepting a stream of connections. It fails if Close ever fails to
// return promptly (the shutdown-hang: a connection accepted concurrently
// with Close escaping the sweep, or wg.Add racing wg.Wait).
func TestServerCloseUnderConnectionBurst(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := NewServer(ln, NewRouter(), "t", slog.New(slog.NewTextHandler(discard{}, nil)))
		go srv.Serve()

		var mu sync.Mutex
		var conns []net.Conn
		stop := make(chan struct{})
		var dialers sync.WaitGroup
		for i := 0; i < 4; i++ {
			dialers.Add(1)
			go func() {
				defer dialers.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					conn, err := net.DialTimeout("tcp", ln.Addr().String(), 100*time.Millisecond)
					if err != nil {
						time.Sleep(200 * time.Microsecond) // listener closed; back off
						continue
					}
					mu.Lock()
					conns = append(conns, conn)
					mu.Unlock()
				}
			}()
		}
		time.Sleep(2 * time.Millisecond) // let connections land mid-accept

		closed := make(chan error, 1)
		go func() { closed <- srv.Close() }()
		select {
		case <-closed:
		case <-time.After(10 * time.Second):
			t.Fatalf("iteration %d: Close did not return (shutdown hang)", iteration)
		}
		close(stop)
		dialers.Wait()
		mu.Lock()
		for _, conn := range conns {
			conn.Close()
		}
		mu.Unlock()
	}
}

// TestUnhandlePrefixWaitsForInFlightDispatch pins the retraction
// contract: UnhandlePrefix does not return while a dispatch to a matching
// route is still in flight, and after it returns the route is gone.
func TestUnhandlePrefixWaitsForInFlightDispatch(t *testing.T) {
	r := NewRouter()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var runs int64
	must(t, r.Handle("app/test/m", func(ctx context.Context, req Request) (any, error) {
		atomic.AddInt64(&runs, 1)
		started <- struct{}{}
		<-release
		return "ok", nil
	}))

	respCh := make(chan Response, 1)
	go func() {
		respCh <- r.Dispatch(context.Background(), Request{ID: 1, Method: "app/test/m"})
	}()
	<-started

	retracted := make(chan struct{})
	go func() { r.UnhandlePrefix("app/"); close(retracted) }()
	select {
	case <-retracted:
		t.Fatal("UnhandlePrefix returned while a dispatch was still in flight")
	case <-time.After(100 * time.Millisecond):
		// Expected: retraction is waiting for the in-flight dispatch.
	}
	close(release)
	select {
	case <-retracted:
	case <-time.After(5 * time.Second):
		t.Fatal("UnhandlePrefix did not return after the dispatch completed")
	}
	resp := <-respCh
	if !resp.OK {
		t.Fatalf("in-flight dispatch failed: %s", resp.Error)
	}
	if got := atomic.LoadInt64(&runs); got != 1 {
		t.Fatalf("handler ran %d times, want 1", got)
	}
	if resp := r.Dispatch(context.Background(), Request{ID: 2, Method: "app/test/m"}); resp.OK {
		t.Fatal("dispatch to retracted route succeeded")
	}
}

// TestNoDispatchRunsAfterRetraction hammers dispatch concurrent with
// retraction: once UnhandlePrefix returns, no handler invocation may
// start (the route-retraction TOCTOU).
func TestNoDispatchRunsAfterRetraction(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		r := NewRouter()
		var mu sync.Mutex
		afterRetraction := 0
		retractedFlag := make(chan struct{})
		release := make(chan struct{})
		must(t, r.Handle("app/x/m", func(ctx context.Context, req Request) (any, error) {
			select {
			case <-release:
			case <-time.After(25 * time.Millisecond):
			}
			select {
			case <-retractedFlag:
				mu.Lock()
				afterRetraction++
				mu.Unlock()
			default:
			}
			return nil, nil
		}))

		stop := make(chan struct{})
		var dispatchers sync.WaitGroup
		for i := 0; i < 4; i++ {
			dispatchers.Add(1)
			go func() {
				defer dispatchers.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					resp := r.Dispatch(context.Background(), Request{ID: 1, Method: "app/x/m"})
					if !resp.OK {
						return // route retracted; retire
					}
				}
			}()
		}
		time.Sleep(time.Millisecond)
		r.UnhandlePrefix("app/")
		close(retractedFlag)
		close(release)
		close(stop)
		dispatchers.Wait()
		mu.Lock()
		bad := afterRetraction
		mu.Unlock()
		if bad != 0 {
			t.Fatalf("iteration %d: %d dispatch(es) ran the handler after retraction returned", iteration, bad)
		}
	}
}

// TestClientCancelWithoutDeadlineAbortsCall: cancelling a context without
// a deadline must abort an in-flight call well inside defaultCallTimeout
// (the default 30 s must not apply to a caller that gave up).
func TestClientCancelWithoutDeadlineAbortsCall(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRouter()
	must(t, r.Handle("test/slow", func(ctx context.Context, req Request) (any, error) {
		time.Sleep(2 * time.Second)
		return "done", nil
	}))
	srv := NewServer(ln, r, "t", slog.New(slog.NewTextHandler(discard{}, nil)))
	defer srv.Close()
	go srv.Serve()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(conn, "t")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err = client.Call(ctx, "test/slow", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancellation not honored: call took %s", elapsed)
	}

	// The aborted call leaves the stream framing unverifiable: the next
	// call must fail fast instead of hanging on a desynchronized line.
	if err := client.Call(context.Background(), "test/slow", nil, nil); err == nil {
		t.Fatal("call on aborted client succeeded, want a broken-connection error")
	}
}
