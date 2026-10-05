package echo

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"gostalgia/internal/app"
	"gostalgia/internal/ipc"
)

func appLaunchContext(t *testing.T) app.LaunchContext {
	t.Helper()
	return app.LaunchContext{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestEchoServesOverRouter registers the application's routes on a fresh
// router and dispatches requests at them, the way a socket client's
// dispatch does.
func TestEchoServesOverRouter(t *testing.T) {
	e, err := Factory(appLaunchContext(t))
	if err != nil {
		t.Fatal(err)
	}
	r := ipc.NewRouter()
	if err := e.RegisterRoutes(r, "app/"+ID); err != nil {
		t.Fatal(err)
	}
	dispatch := func(id int64, method string, params string) ipc.Response {
		t.Helper()
		return r.Dispatch(context.Background(), ipc.Request{
			ID:     id,
			Method: "app/" + ID + "/" + method,
			Params: json.RawMessage(params),
		})
	}

	resp := dispatch(1, "echo", `{"msg":"hello environment"}`)
	if !resp.OK {
		t.Fatalf("echo failed: %s", resp.Error)
	}
	var out struct {
		Msg    string `json:"msg"`
		Echoes int64  `json:"echoes"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Msg != "hello environment" || out.Echoes != 1 {
		t.Fatalf("echo response = %+v", out)
	}

	// The counter increments across calls; stats reports the same value.
	resp = dispatch(2, "echo", `{"msg":"again"}`)
	if !resp.OK {
		t.Fatalf("second echo failed: %s", resp.Error)
	}
	resp = dispatch(3, "stats", "")
	if !resp.OK {
		t.Fatalf("stats failed: %s", resp.Error)
	}
	var stats struct {
		Echoes        int64   `json:"echoes"`
		UptimeSeconds float64 `json:"uptime_seconds"`
	}
	if err := json.Unmarshal(resp.Data, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Echoes != 2 {
		t.Errorf("stats echoes = %d, want 2", stats.Echoes)
	}
	if stats.UptimeSeconds < 0 {
		t.Errorf("stats uptime = %f, want non-negative", stats.UptimeSeconds)
	}

	// Bad input is a response error, not a panic.
	if resp := dispatch(4, "echo", `{}`); resp.OK {
		t.Error("echo without params.msg succeeded, want error")
	}
	if resp := dispatch(5, "echo", `{not json`); resp.OK {
		t.Error("echo with malformed params succeeded, want error")
	}
	if resp := r.Dispatch(context.Background(), ipc.Request{ID: 6, Method: "app/" + ID + "/missing"}); resp.OK {
		t.Error("unknown route dispatch succeeded, want unknown-method error")
	}
}
