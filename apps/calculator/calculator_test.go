package calculator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gostalgia/internal/app"
	"gostalgia/internal/events"
	"gostalgia/internal/ipc"
	"gostalgia/internal/process"
	"gostalgia/internal/security"
	"gostalgia/sdk"
)

// launchCalc boots the real application through the real Manager, so its
// routes reach the router exactly as a launch publishes them.
func launchCalc(t *testing.T) (*app.Manager, *ipc.Router) {
	t.Helper()
	bus := events.NewBus()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := ipc.NewRouter()
	reg := app.NewRegistry()
	if err := reg.RegisterBuiltin(Manifest(), Factory); err != nil {
		t.Fatal(err)
	}
	mgr := app.NewManager(reg, process.NewManager(bus, log), router, bus, log)
	if _, err := mgr.Launch(context.Background(), ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Stop(ID, 2*time.Second) })
	return mgr, router
}

// dispatch sends an IPC request with the calculator's ipc capability.
func dispatch(t *testing.T, r *ipc.Router, id int64, method string, params string) ipc.Response {
	t.Helper()
	ctx := ipc.WithCapabilities(context.Background(), security.NewCapabilities(security.CapIPC))
	return r.Dispatch(ctx, ipc.Request{
		ID:     id,
		Method: "app/" + ID + "/" + method,
		Params: json.RawMessage(params),
	})
}

func mustView(t *testing.T, data json.RawMessage) sdk.View {
	t.Helper()
	var v sdk.View
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal view: %v", err)
	}
	if err := v.Validate(); err != nil {
		t.Fatalf("view validation: %v", err)
	}
	return v
}

func TestManifestValidity(t *testing.T) {
	m := Manifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest validation: %v", err)
	}
	if m.ID != ID {
		t.Errorf("id = %s, want %s", m.ID, ID)
	}
	if m.Entrypoint != "calculator" {
		t.Errorf("entrypoint = %s, want calculator", m.Entrypoint)
	}
	if !json.Valid(ManifestJSON()) {
		t.Error("ManifestJSON is not valid JSON")
	}
	if len(m.Permissions) != 1 || m.Permissions[0] != "ipc" {
		t.Errorf("permissions = %v, want [ipc]", m.Permissions)
	}
}

// TestEvaluatePure checks the pure arithmetic core without any IPC surface.
func TestEvaluatePure(t *testing.T) {
	tests := []struct {
		expr    string
		want    string
		wantErr bool
	}{
		{"1+2+3", "6", false},
		{"10-2-3", "5", false},
		{"2*3+4", "10", false}, // left-to-right, not precedence
		{"2+3*4", "20", false}, // left-to-right, not precedence
		{"5/2", "2.5", false},
		{"1/3", "0.3333333333333333", false},
		{"0.1+0.2", "0.3", false},
		{".5+.5", "1", false},
		{"1.+", "", true},                 // malformed
		{"1++", "", true},                 // malformed
		{"1/0", "", true},                 // divide by zero
		{"1/0.0", "", true},               // divide by zero
		{"99999999999999999+1", "", true}, // overflow on parse
		{"9999999999999999*10", "", true}, // overflow on result
	}

	for _, tc := range tests {
		got, err := Evaluate(tc.expr)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Evaluate(%q) = %q, want error", tc.expr, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Evaluate(%q): %v", tc.expr, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Evaluate(%q) = %q, want %q", tc.expr, got, tc.want)
		}
	}
}

func TestCalcRoute(t *testing.T) {
	_, r := launchCalc(t)

	resp := dispatch(t, r, 1, "calc", `{"expr":"1+2+3"}`)
	if !resp.OK {
		t.Fatalf("calc failed: %s", resp.Error)
	}
	var out struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Result != "6" {
		t.Errorf("result = %s, want 6", out.Result)
	}

	// Arithmetic errors are returned as honest IPC errors, not panics.
	for _, expr := range []string{`{"expr":"1/0"}`, `{"expr":"1++"}`, `{"expr":""}`} {
		if resp := dispatch(t, r, 2, "calc", expr); resp.OK {
			t.Errorf("calc(%s) succeeded, want error", expr)
		}
	}
}

func TestActionBoundAndBasicArithmetic(t *testing.T) {
	_, r := launchCalc(t)

	// Initial view.
	resp := dispatch(t, r, 1, "view", `{"version":1}`)
	if !resp.OK {
		t.Fatalf("view failed: %s", resp.Error)
	}
	v := mustView(t, resp.Data)
	if len(v.Actions) != 16 {
		t.Errorf("actions = %d, want 16", len(v.Actions))
	}
	if v.Items[0].Detail != "0" {
		t.Errorf("initial display = %q, want 0", v.Items[0].Detail)
	}

	// Press: 1 + 2 =
	seq := 0
	doAction := func(action string) sdk.View {
		seq++
		req, _ := json.Marshal(sdk.ActionRequest{
			Version:   1,
			Instance:  v.Instance,
			RequestID: fmt.Sprintf("r%d", seq),
			Action:    action,
		})
		resp := dispatch(t, r, int64(seq+10), "action", string(req))
		if !resp.OK {
			t.Fatalf("action %s failed: %s", action, resp.Error)
		}
		return mustView(t, resp.Data)
	}

	v = doAction("digit_1")
	v = doAction("add")
	v = doAction("digit_2")
	v = doAction("eq")
	if v.Items[0].Detail != "3" {
		t.Errorf("1+2 = %q, want 3", v.Items[0].Detail)
	}

	// Chained: + 3 =
	v = doAction("add")
	v = doAction("digit_3")
	v = doAction("eq")
	if v.Items[0].Detail != "6" {
		t.Errorf("3+3 = %q, want 6", v.Items[0].Detail)
	}
}

func TestDivideByZeroAndClear(t *testing.T) {
	_, r := launchCalc(t)

	resp := dispatch(t, r, 1, "view", `{"version":1}`)
	if !resp.OK {
		t.Fatalf("view failed: %s", resp.Error)
	}
	v := mustView(t, resp.Data)

	seq := 0
	doAction := func(action string) sdk.View {
		seq++
		req, _ := json.Marshal(sdk.ActionRequest{
			Version:   1,
			Instance:  v.Instance,
			RequestID: fmt.Sprintf("r%d", seq),
			Action:    action,
		})
		resp := dispatch(t, r, int64(seq+10), "action", string(req))
		if !resp.OK {
			t.Fatalf("action %s failed: %s", action, resp.Error)
		}
		return mustView(t, resp.Data)
	}

	_ = doAction("digit_1")
	_ = doAction("div")
	_ = doAction("digit_0")
	v = doAction("eq")
	if v.State != sdk.ViewError || !strings.Contains(v.Error, "divide by zero") {
		t.Errorf("divide by zero state = %q error = %q, want ViewError with divide-by-zero", v.State, v.Error)
	}

	v = doAction("clear")
	if v.State != sdk.ViewReady || v.Error != "" || v.Items[0].Detail != "0" {
		t.Errorf("after clear: state=%q error=%q detail=%q, want ready, no error, 0", v.State, v.Error, v.Items[0].Detail)
	}
}

func TestActionValidationRejectsUnknownActions(t *testing.T) {
	_, r := launchCalc(t)

	resp := dispatch(t, r, 1, "view", `{"version":1}`)
	if !resp.OK {
		t.Fatalf("view failed: %s", resp.Error)
	}
	var v sdk.View
	if err := json.Unmarshal(resp.Data, &v); err != nil {
		t.Fatal(err)
	}

	req, _ := json.Marshal(sdk.ActionRequest{
		Version:   1,
		Instance:  v.Instance,
		RequestID: "r1",
		Action:    "launch_missiles",
	})
	if resp := dispatch(t, r, 2, "action", string(req)); resp.OK {
		t.Error("unknown action succeeded, want error")
	}
}

func TestConcurrentCalls(t *testing.T) {
	_, r := launchCalc(t)

	var wg sync.WaitGroup
	errs := make(chan string, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			resp := dispatch(t, r, int64(n), "calc", `{"expr":"2+3*4"}`)
			if !resp.OK {
				errs <- fmt.Sprintf("calc failed: %s", resp.Error)
				return
			}
			var out struct {
				Result string `json:"result"`
			}
			if err := json.Unmarshal(resp.Data, &out); err != nil {
				errs <- fmt.Sprintf("unmarshal: %v", err)
				return
			}
			if out.Result != "20" {
				errs <- fmt.Sprintf("result = %s, want 20", out.Result)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestRelaunchReset(t *testing.T) {
	bus := events.NewBus()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := ipc.NewRouter()
	reg := app.NewRegistry()
	if err := reg.RegisterBuiltin(Manifest(), Factory); err != nil {
		t.Fatal(err)
	}
	mgr := app.NewManager(reg, process.NewManager(bus, log), router, bus, log)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p, err := mgr.Launch(ctx, ID)
	if err != nil {
		t.Fatal(err)
	}

	resp := dispatch(t, router, 1, "view", `{"version":1}`)
	if !resp.OK {
		t.Fatalf("view failed: %s", resp.Error)
	}
	var v sdk.View
	if err := json.Unmarshal(resp.Data, &v); err != nil {
		t.Fatal(err)
	}

	req, _ := json.Marshal(sdk.ActionRequest{
		Version:   1,
		Instance:  v.Instance,
		RequestID: "r1",
		Action:    "digit_5",
	})
	resp = dispatch(t, router, 2, "action", string(req))
	if !resp.OK {
		t.Fatalf("action failed: %s", resp.Error)
	}

	if err := mgr.Stop(ID, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("first launch did not exit")
	}

	// Relaunch: the instance must start fresh.
	p2, err := mgr.Launch(ctx, ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = mgr.Stop(ID, 2*time.Second)
		<-p2.Done()
	}()

	resp = dispatch(t, router, 3, "view", `{"version":1}`)
	if !resp.OK {
		t.Fatalf("view after relaunch failed: %s", resp.Error)
	}
	v = mustView(t, resp.Data)
	if v.Items[0].Detail != "0" {
		t.Errorf("after relaunch display = %q, want 0", v.Items[0].Detail)
	}
}
