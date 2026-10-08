package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goRuntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"gostalgia/internal/app"
	"gostalgia/internal/process"
	"gostalgia/internal/runtime"
	"gostalgia/sdk"
)

var (
	buildExtOnce sync.Once
	extBinPath   string
	buildExtErr  error
)

func getExternalBinary(t *testing.T) string {
	t.Helper()
	buildExtOnce.Do(func() {
		tmpDir, err := os.MkdirTemp("", "ext-bin-*")
		if err != nil {
			buildExtErr = err
			return
		}
		bin := filepath.Join(tmpDir, "external-app")
		if goRuntime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "gostalgia/test/testapps/external")
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildExtErr = fmt.Errorf("build external-app: %w\n%s", err, string(out))
			return
		}
		extBinPath = bin
	})
	if buildExtErr != nil {
		t.Fatalf("failed to build external test binary: %v", buildExtErr)
	}
	return extBinPath
}

func TestExternalApplicationE2E(t *testing.T) {
	bin := getExternalBinary(t)

	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("external e2e test") })

	extMan := app.Manifest{
		ID:              "com.test.externalrunner",
		Name:            "External Runner",
		Version:         "1.0.0",
		Mode:            sdk.ModeExternal,
		Executable:      bin,
		Permissions:     []string{sdk.CapIPC},
		ProtocolVersion: 1,
	}
	must(t, rt.Apps.Registry().RegisterExternal(extMan))

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Launch external app over authenticated IPC
	var launchRes struct {
		ID  string `json:"id"`
		PID int32  `json:"pid"`
	}
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": extMan.ID}, &launchRes))
	if launchRes.PID <= 0 {
		t.Fatalf("unexpected launch PID %d", launchRes.PID)
	}

	// 2. Verify in app/list
	var appList []app.Status
	must(t, client.Call(ctx, "app/list", nil, &appList))
	foundApp := false
	for _, a := range appList {
		if a.Manifest.ID == extMan.ID {
			foundApp = true
			if !a.Running || a.PID != launchRes.PID {
				t.Fatalf("app/list status unexpected: %+v", a)
			}
		}
	}
	if !foundApp {
		t.Fatal("external app not found in app/list")
	}

	// 3. Verify in proc/list
	var procList []process.Info
	must(t, client.Call(ctx, "proc/list", nil, &procList))
	foundProc := false
	for _, p := range procList {
		if p.ID == launchRes.PID {
			foundProc = true
			if p.Kind != process.KindChild {
				t.Fatalf("proc/list kind = %s, want child", p.Kind)
			}
		}
	}
	if !foundProc {
		t.Fatal("external app PID not found in proc/list")
	}

	// 4. Drive presentation contract over IPC without terminal ownership
	const base = "app/com.test.externalrunner/"
	var view sdk.View
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	must(t, view.Validate())
	if view.Status != "initialized" {
		t.Fatalf("initial view status = %q, want initialized", view.Status)
	}

	actionReq := sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "req1",
		Action:    "set_state",
		Values:    map[string]string{"state": "running-e2e"},
	}
	must(t, client.Call(ctx, base+"action", actionReq, &view))
	must(t, view.Validate())
	if view.Status != "running-e2e" {
		t.Fatalf("updated view status = %q, want running-e2e", view.Status)
	}

	// 4b. Version-2 elements negotiate; cell actions route semantically.
	var v2 sdk.View
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 2}, &v2))
	must(t, v2.Validate())
	if v2.Version != 2 || len(v2.Blocks) != 1 || len(v2.Meters) != 2 || v2.Grid == nil || len(v2.Grid.Cells) != 6 {
		t.Fatalf("version-2 view missing elements: %+v", v2)
	}
	press := sdk.ActionRequest{Version: 2, Instance: v2.Instance, RequestID: "press1", Action: "press", CellID: "feed"}
	must(t, client.Call(ctx, base+"action", press, &v2))
	if v2.Status != "pressed feed" || len(v2.Grid.Cells) != 6 {
		t.Fatalf("cell action snapshot: %+v", v2)
	}
	for _, p := range []sdk.ActionRequest{
		{Version: 2, Instance: v2.Instance, RequestID: "press2", Action: "press"},
		{Version: 2, Instance: v2.Instance, RequestID: "press3", Action: "press", CellID: "nap"},
		{Version: 2, Instance: v2.Instance, RequestID: "press4", Action: "press", CellID: "cell-404"},
		{Version: 1, Instance: v2.Instance, RequestID: "press5", Action: "press", CellID: "feed"},
	} {
		if err := client.Call(ctx, base+"action", p, nil); err == nil {
			t.Fatalf("invalid cell action accepted: %+v", p)
		}
	}
	must(t, client.Call(ctx, base+"action", sdk.ActionRequest{
		Version: 2, Instance: v2.Instance, RequestID: "reset1", Action: "reset",
	}, &v2))
	// A version-1 request must not carry version-2 elements.
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	if view.Version != 1 || len(view.Blocks) != 0 || len(view.Meters) != 0 || view.Grid != nil {
		t.Fatalf("version-1 view leaked version-2 elements: %+v", view)
	}
	if view.Status != "initialized" {
		t.Fatalf("reset action state = %q, want initialized", view.Status)
	}

	// 5. Verify stdout log capture into bounded ring buffers via proc/logs
	var logs struct {
		ID     int32                     `json:"id"`
		Name   string                    `json:"name"`
		Stdout process.StreamDiagnostics `json:"stdout"`
	}
	must(t, client.Call(ctx, "proc/logs", map[string]any{"id": launchRes.PID, "stream": "stdout"}, &logs))
	if !strings.Contains(logs.Stdout.Content, "external app started") {
		t.Fatalf("expected stdout to contain 'external app started', got content: %q", logs.Stdout.Content)
	}

	// 6. Stop app over IPC
	must(t, client.Call(ctx, "app/stop", map[string]string{"id": extMan.ID}, nil))

	// 7. Verify route retraction
	if err := client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view); err == nil {
		t.Fatal("expected view route to fail after stop")
	}

	// 8. Relaunch app over IPC and verify fresh state
	var relaunchRes struct {
		ID  string `json:"id"`
		PID int32  `json:"pid"`
	}
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": extMan.ID}, &relaunchRes))
	if relaunchRes.PID <= 0 || relaunchRes.PID == launchRes.PID {
		t.Fatalf("unexpected relaunch PID %d", relaunchRes.PID)
	}

	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	if view.Status != "initialized" {
		t.Fatalf("relaunch view status = %q, want fresh initialized", view.Status)
	}

	must(t, client.Call(ctx, "app/stop", map[string]string{"id": extMan.ID}, nil))
}
