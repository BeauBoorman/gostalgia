package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gostalgia/internal/ipc"
	"gostalgia/internal/process"
	"gostalgia/internal/security"
	"gostalgia/sdk"
)

type screenInstance struct {
	exit          chan struct{}
	mode          string
	actionStarted chan struct{}
	actionDone    chan struct{}
	stops         atomic.Int32
}

func (s *screenInstance) Init(c *sdk.Context) error {
	err := c.Present(func(context.Context) (sdk.View, error) {
		return sdk.View{Title: "Screen", State: sdk.ViewReady, Actions: []sdk.Action{{ID: "wait", Label: "Wait"}}}, nil
	}, func(ctx context.Context, p sdk.ActionRequest) (sdk.View, error) {
		close(s.actionStarted)
		<-ctx.Done()
		close(s.actionDone)
		return sdk.View{}, ctx.Err()
	})
	if s.mode == "init-fail" && err == nil {
		return errors.New("failed after declaring presentation")
	}
	return err
}

func (s *screenInstance) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case <-s.exit:
		switch s.mode {
		case "fail":
			return errors.New("screen crashed")
		case "panic":
			panic("screen panic")
		}
		return nil
	}
}

func (s *screenInstance) Stop(context.Context) error { s.stops.Add(1); return nil }

func launchScreen(t *testing.T, mode string) (*Manager, *ipc.Router, *screenInstance, *process.Process) {
	t.Helper()
	m, r := newTestManager(t)
	man := fakeManifest
	man.ID, man.Entrypoint = "com.test.screen", "screen"
	s := &screenInstance{mode: mode, exit: make(chan struct{}), actionStarted: make(chan struct{}), actionDone: make(chan struct{})}
	must(t, m.reg.RegisterBuiltin(man, func() (sdk.Instance, error) { return s, nil }))
	p, err := m.Launch(context.Background(), man.ID)
	if mode == "init-fail" {
		if err == nil {
			t.Fatal("failed Init launched")
		}
	} else {
		must(t, err)
	}
	t.Cleanup(func() { _ = m.Stop(man.ID, time.Second) })
	return m, r, s, p
}

func TestPresentationRoutesRetractOnEveryExit(t *testing.T) {
	for _, mode := range []string{"stop", "natural", "fail", "panic", "init-fail"} {
		t.Run(mode, func(t *testing.T) {
			m, r, s, p := launchScreen(t, mode)
			if p != nil {
				ctx := ipc.WithCapabilities(context.Background(), security.AdminCapabilities())
				resp := r.Dispatch(ctx, ipc.Request{Method: "app/com.test.screen/view", Params: json.RawMessage(`{"version":1}`)})
				if !resp.OK {
					t.Fatal(resp.Error)
				}
				var view sdk.View
				must(t, json.Unmarshal(resp.Data, &view))
				must(t, view.Validate())
				if mode == "stop" {
					must(t, m.Stop("com.test.screen", time.Second))
				} else {
					close(s.exit)
				}
				select {
				case <-p.Done():
				case <-time.After(2 * time.Second):
					t.Fatal("app cleanup did not complete")
				}
			}
			if m.IsRunning("com.test.screen") || s.stops.Load() != 1 {
				t.Fatal("app state or Stop lifecycle not retracted")
			}
			for _, method := range r.Methods() {
				if strings.HasPrefix(method, "app/com.test.screen/") {
					t.Fatalf("presentation route survived %s: %s", mode, method)
				}
			}
		})
	}
}

func TestPresentationCancellationOverSocket(t *testing.T) {
	for _, mode := range []string{"cancel", "stop", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			m, router, screen, _ := launchScreen(t, "")
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			must(t, err)
			server := ipc.NewServer(listener, router, "test-token", slog.New(slog.NewTextHandler(io.Discard, nil)))
			served := make(chan error, 1)
			go func() { served <- server.Serve() }()
			t.Cleanup(func() { _ = server.Close(); <-served })
			conn, err := net.Dial("tcp", listener.Addr().String())
			must(t, err)
			client, err := ipc.NewClient(conn, "test-token")
			must(t, err)
			t.Cleanup(func() { client.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			const base = "app/com.test.screen/"
			var view sdk.View
			must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
			p := sdk.ActionRequest{Version: 1, Instance: view.Instance, RequestID: "wait", Action: "wait"}
			done := make(chan error, 1)
			go func() { done <- client.Call(ctx, base+"action", p, nil) }()
			select {
			case <-screen.actionStarted:
			case <-ctx.Done():
				t.Fatal("action did not reach callback")
			}
			switch mode {
			case "cancel":
				must(t, client.Call(ctx, base+"cancel", sdk.CancelRequest{Version: 1, Instance: view.Instance, RequestID: p.RequestID}, nil))
			case "stop":
				must(t, m.Stop("com.test.screen", time.Second))
			case "disconnect":
				client.Close()
			}
			select {
			case <-screen.actionDone:
			case <-ctx.Done():
				t.Fatal("server action did not observe cancellation")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("canceled action succeeded")
				}
			case <-ctx.Done():
				t.Fatal("action client wait did not finish")
			}
			if mode == "cancel" {
				must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
			}
		})
	}
}
