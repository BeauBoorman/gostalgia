package e2e

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"gostalgia/internal/runtime"
	"gostalgia/sdk"
)

// This test uses exactly the routes used by the shell, over an authenticated
// socket, without importing or rendering a UI.
func TestHeadlessEchoPresentation(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, LogOutput: io.Discard})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("presentation test") })
	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const base = "app/com.gostalgia.echo/"
	var view sdk.View
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	must(t, view.Validate())
	first := view.Instance
	p := sdk.ActionRequest{Version: 1, Instance: first, RequestID: "headless", Action: "echo", Values: map[string]string{"msg": "no terminal required"}}
	must(t, client.Call(ctx, base+"action", p, &view))
	if len(view.Items) != 1 || view.Items[0].Detail != "no terminal required" || view.Status != "1 echoes this launch" {
		t.Fatalf("action snapshot: %+v", view)
	}
	var stats struct {
		Echoes int `json:"echoes"`
	}
	must(t, client.Call(ctx, base+"stats", nil, &stats))
	if stats.Echoes != 1 {
		t.Fatal("presentation action did not use the Echo operation")
	}
	must(t, client.Call(ctx, base+"echo", map[string]string{"msg": "legacy client"}, nil))
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	if view.Status != "2 echoes this launch" || view.Items[0].Detail != "legacy client" {
		t.Fatal("programmatic operation did not update presentation")
	}
	must(t, client.Call(ctx, base+"echo", map[string]string{"msg": strings.Repeat("界", 3000)}, nil))
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	must(t, view.Validate())
	if !strings.HasSuffix(view.Items[0].Detail, "...") {
		t.Fatal("large legacy operation output did not fit the presentation contract")
	}
	p.RequestID = "early"
	must(t, client.Call(ctx, base+"cancel", sdk.CancelRequest{Version: 1, Instance: first, RequestID: p.RequestID}, nil))
	if err := client.Call(ctx, base+"action", p, nil); err == nil {
		t.Fatal("canceled action executed over IPC")
	}
	must(t, client.Call(ctx, "app/stop", map[string]string{"id": "com.gostalgia.echo"}, nil))
	for _, method := range []string{"view", "action", "cancel"} {
		if err := client.Call(ctx, base+method, p, nil); err == nil {
			t.Fatalf("%s route survived stop", method)
		}
	}
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": "com.gostalgia.echo"}, nil))
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	if view.Instance == first || view.Status != "0 echoes this launch" || len(view.Items) != 0 {
		t.Fatal("relaunch reused presentation instance or state")
	}
	if err := client.Call(ctx, base+"action", p, nil); err == nil {
		t.Fatal("previous-launch action reached new instance")
	}
}
