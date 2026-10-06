package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
