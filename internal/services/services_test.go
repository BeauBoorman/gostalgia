package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gostalgia/apps"
	"gostalgia/internal/app"
	"gostalgia/internal/config"
	"gostalgia/internal/events"
	"gostalgia/internal/ipc"
	"gostalgia/internal/process"
	"gostalgia/internal/security"
	"gostalgia/internal/service"
	"gostalgia/internal/session"
	"gostalgia/internal/vfs"
)

// testEnv is a full core-service stack without sockets or a real boot.
type testEnv struct {
	t      *testing.T
	ctx    *service.Context
	router *ipc.Router
	sm     *service.Manager
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	bus := events.NewBus()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := ipc.NewRouter()

	// The real runtime creates the layout in InitRoot before opening
	// the host-backed root; mirror that here.
	must(t, os.MkdirAll(filepath.Join(root, "vfs"), 0o755))
	hostFS, err := vfs.NewHost(filepath.Join(root, "vfs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hostFS.Close() })
	env := vfs.New(hostFS)
	must(t, env.Mount("/tmp", vfs.NewMem()))
	for _, dir := range []string{"/users/guest/documents", "/apps/manifests"} {
		must(t, env.MkdirAll(dir))
	}

	cfg, err := config.Load(filepath.Join(root, "config", "system.json"))
	must(t, err)

	procs := process.NewManager(bus, log)
	sessions := session.NewManager(bus, log)
	registry := app.NewRegistry()
	must(t, apps.Register(registry))
	appMgr := app.NewManager(registry, procs, router, bus, log)

	sctx := &service.Context{
		Root:     root,
		Version:  "test",
		Config:   cfg,
		Events:   bus,
		Log:      log,
		Router:   router,
		VFS:      env,
		Procs:    procs,
		Apps:     appMgr,
		Sessions: sessions,
		Token:    "test-token",
		BootedAt: time.Now(),
		Shutdown: func(reason string) {},
	}
	sm := service.NewManager(sctx, bus, log)
	sctx.Services = sm
	must(t, sm.Register(NewSys()))
	must(t, sm.Register(NewProc()))
	must(t, sm.Register(NewFS()))
	must(t, sm.StartAll(context.Background()))
	t.Cleanup(func() { _ = sm.StopAll(context.Background()) })
	// Mirror the runtime: a default user session exists.
	_, err = sessions.Create(security.User{ID: "u-guest", Name: "guest"})
	must(t, err)
	return &testEnv{t: t, ctx: sctx, router: router, sm: sm}
}

func (e *testEnv) call(ctx context.Context, caps *security.Capabilities, method string, params any) ipc.Response {
	e.t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			e.t.Fatal(err)
		}
		raw = b
	}
	if caps != nil {
		ctx = ipc.WithCapabilities(ctx, caps)
	}
	return e.router.Dispatch(ctx, ipc.Request{ID: 1, Method: method, Params: raw})
}

func TestSysStatus(t *testing.T) {
	env := newTestEnv(t)
	resp := env.call(context.Background(), security.AdminCapabilities(), "sys/status", nil)
	if !resp.OK {
		t.Fatalf("sys/status failed: %s", resp.Error)
	}
	var status struct {
		Version  string `json:"version"`
		Services []struct {
			Name  string        `json:"name"`
			State service.State `json:"state"`
		} `json:"services"`
		Apps []app.Status `json:"apps"`
	}
	must(t, json.Unmarshal(resp.Data, &status))
	if status.Version != "test" {
		t.Errorf("version = %q, want test", status.Version)
	}
	if len(status.Services) != 3 {
		t.Errorf("services = %d, want 3", len(status.Services))
	}
	if len(status.Apps) != 1 || status.Apps[0].Manifest.ID != "com.gostalgia.echo" {
		t.Errorf("apps = %+v, want echo", status.Apps)
	}
}

func TestSysShutdownRequiresCapability(t *testing.T) {
	env := newTestEnv(t)
	resp := env.call(context.Background(), security.NewCapabilities(security.CapIPC), "sys/shutdown", nil)
	if resp.OK {
		t.Fatal("shutdown without the shutdown capability succeeded")
	}
	if resp.Error == "" {
		t.Fatal("shutdown denial has no error message")
	}
}

func TestWhoamiReportsCallerCapabilities(t *testing.T) {
	env := newTestEnv(t)
	admin := security.AdminCapabilities()
	resp := env.call(context.Background(), admin, "session/whoami", nil)
	if !resp.OK {
		t.Fatalf("whoami failed: %s", resp.Error)
	}
	var out struct {
		User         string   `json:"user"`
		Capabilities []string `json:"capabilities"`
	}
	must(t, json.Unmarshal(resp.Data, &out))
	if out.User != "guest" {
		t.Errorf("user = %q, want guest", out.User)
	}
	if len(out.Capabilities) == 0 {
		t.Error("capabilities list is empty")
	}
}

func TestFSWriteReadRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	admin := security.AdminCapabilities()

	payload := []byte("hello from the fs service")
	encoded := base64.StdEncoding.EncodeToString(payload)
	resp := env.call(context.Background(), admin, "fs/write", map[string]string{
		"path":        "/users/guest/documents/note.txt",
		"data_base64": encoded,
	})
	if !resp.OK {
		t.Fatalf("fs/write failed: %s", resp.Error)
	}

	resp = env.call(context.Background(), admin, "fs/read", map[string]string{"path": "/users/guest/documents/note.txt"})
	if !resp.OK {
		t.Fatalf("fs/read failed: %s", resp.Error)
	}
	var out struct {
		Data string `json:"data_base64"`
	}
	must(t, json.Unmarshal(resp.Data, &out))
	decoded, err := base64.StdEncoding.DecodeString(out.Data)
	must(t, err)
	if string(decoded) != string(payload) {
		t.Fatalf("round trip mismatch: %q != %q", decoded, payload)
	}

	// The write landed in the host-backed root.
	host, err := os.ReadFile(filepath.Join(env.ctx.Root, "vfs", "users/guest/documents/note.txt"))
	must(t, err)
	if string(host) != string(payload) {
		t.Fatalf("host copy = %q, want %q", host, payload)
	}
}

func TestFSWriteDeniedWithoutCapability(t *testing.T) {
	env := newTestEnv(t)
	readOnly := security.NewCapabilities(security.CapFileRead)
	resp := env.call(context.Background(), readOnly, "fs/write", map[string]string{
		"path":        "/users/guest/documents/nope.txt",
		"data_base64": "",
	})
	if resp.OK {
		t.Fatal("fs/write succeeded without the fs.write capability")
	}
}

func TestFSEscapeRejected(t *testing.T) {
	env := newTestEnv(t)
	admin := security.AdminCapabilities()
	resp := env.call(context.Background(), admin, "fs/write", map[string]string{
		"path":        "/../escaped.txt",
		"data_base64": "",
	})
	if resp.OK {
		t.Fatal("fs/write escaped the environment root")
	}
}

func TestProcListAndStop(t *testing.T) {
	env := newTestEnv(t)
	admin := security.AdminCapabilities()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proc, err := env.ctx.Apps.Launch(ctx, "com.gostalgia.echo")
	must(t, err)

	resp := env.call(context.Background(), admin, "proc/list", nil)
	if !resp.OK {
		t.Fatalf("proc/list failed: %s", resp.Error)
	}
	var procs []struct {
		ID    int32  `json:"id"`
		Name  string `json:"name"`
		State string `json:"state"`
	}
	must(t, json.Unmarshal(resp.Data, &procs))
	found := false
	for _, p := range procs {
		if p.ID == proc.ID() && p.Name == "com.gostalgia.echo" && p.State == "running" {
			found = true
		}
	}
	if !found {
		t.Fatalf("echo process missing from proc/list: %+v", procs)
	}

	resp = env.call(context.Background(), admin, "proc/stop", map[string]int{"id": int(proc.ID())})
	if !resp.OK {
		t.Fatalf("proc/stop failed: %s", resp.Error)
	}
	select {
	case <-proc.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("echo process did not exit after proc/stop")
	}
}

func TestAppLaunchViaIPC(t *testing.T) {
	env := newTestEnv(t)
	admin := security.AdminCapabilities()

	// The runtime already launches echo at boot; in the test env it is
	// not launched, so launching here must succeed once, then fail.
	resp := env.call(context.Background(), admin, "app/launch", map[string]string{"id": "com.gostalgia.echo"})
	if !resp.OK {
		t.Fatalf("app/launch failed: %s", resp.Error)
	}
	resp = env.call(context.Background(), admin, "app/launch", map[string]string{"id": "com.gostalgia.echo"})
	if resp.OK {
		t.Fatal("second launch succeeded, single-instance policy broken")
	}
	if err := env.ctx.Apps.Stop("com.gostalgia.echo", 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestAppControlPermissionsAndRequestLifetime(t *testing.T) {
	env := newTestEnv(t)
	for _, method := range []string{"app/launch", "app/stop"} {
		resp := env.call(context.Background(), security.NewCapabilities(security.CapIPC), method, map[string]string{"id": "com.gostalgia.echo"})
		if resp.OK {
			t.Fatalf("%s without capability succeeded", method)
		}
	}
	request, cancel := context.WithCancel(context.Background())
	resp := env.call(request, security.AdminCapabilities(), "app/launch", map[string]string{"id": "com.gostalgia.echo"})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	cancel() // A client's request/disconnection cannot own the app lifetime.
	resp = env.call(context.Background(), security.AdminCapabilities(), "app/com.gostalgia.echo/identity", nil)
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	var out struct {
		Capabilities []string `json:"capabilities"`
	}
	must(t, json.Unmarshal(resp.Data, &out))
	if len(out.Capabilities) != 1 || out.Capabilities[0] != security.CapIPC {
		t.Fatalf("caps = %v", out.Capabilities)
	}
	resp = env.call(context.Background(), security.AdminCapabilities(), "app/stop", map[string]string{"id": "com.gostalgia.echo"})
	if !resp.OK || env.ctx.Apps.IsRunning("com.gostalgia.echo") {
		t.Fatalf("stop = %+v", resp)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
