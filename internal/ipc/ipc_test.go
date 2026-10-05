package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
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
