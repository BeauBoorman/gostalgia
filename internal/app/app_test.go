package app

import (
	"context"
	"encoding/json"
	"errors"
	"gostalgia/sdk"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"gostalgia/internal/events"
	"gostalgia/internal/ipc"
	"gostalgia/internal/process"
	"gostalgia/internal/security"
)

// fakeInstance is a minimal application: blocks until stopped, serves one
// counting route.
type fakeInstance struct {
	mu    sync.Mutex
	pings int
}

func (f *fakeInstance) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (f *fakeInstance) Stop(ctx context.Context) error { return nil }

func (f *fakeInstance) Init(c *sdk.Context) error {
	return c.Handle("ping", func(ctx context.Context, raw json.RawMessage) (any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.pings++
		return map[string]int{"pings": f.pings}, nil
	})
}

var fakeManifest = Manifest{
	ID:          "com.test.fake",
	Name:        "Fake",
	Version:     "1.0.0",
	Entrypoint:  "fake",
	Permissions: []string{sdk.CapIPC},
}

func newTestManager(t *testing.T) (*Manager, *ipc.Router) {
	t.Helper()
	bus := events.NewBus()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := ipc.NewRouter()
	procs := process.NewManager(bus, log)
	reg := NewRegistry()
	must(t, reg.RegisterBuiltin(fakeManifest, func() (Instance, error) {
		return &fakeInstance{}, nil
	}))
	return NewManager(reg, procs, router, bus, log), router
}

func TestRegistryValidation(t *testing.T) {
	reg := NewRegistry()
	for _, bad := range []Manifest{
		{ID: "notdns", Name: "x", Version: "1", Entrypoint: "e"},
		{ID: "com.test.x", Name: "", Version: "1", Entrypoint: "e"},
		{ID: "com.test.x", Name: "x", Version: "", Entrypoint: "e"},
		{ID: "com.test.x", Name: "x", Version: "1", Entrypoint: ""},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("manifest %+v passed validation", bad)
		}
	}
	good := fakeManifest
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	must(t, reg.RegisterBuiltin(good, func() (Instance, error) { return &fakeInstance{}, nil }))
	if err := reg.RegisterBuiltin(good, func() (Instance, error) { return &fakeInstance{}, nil }); err == nil {
		t.Fatal("duplicate id accepted")
	}
	dupEntry := fakeManifest
	dupEntry.ID = "com.test.other"
	if err := reg.RegisterBuiltin(dupEntry, func() (Instance, error) { return &fakeInstance{}, nil }); err == nil {
		t.Fatal("duplicate entrypoint accepted")
	}
}

func TestRegistryLoadManifests(t *testing.T) {
	reg := NewRegistry()
	fmap := fstest.MapFS{
		"apps/manifests/com.example.good.json": &fstest.MapFile{
			Data: []byte(`{"id":"com.example.good","name":"Good","version":"0.2.0","entrypoint":"good"}`),
		},
		"apps/manifests/broken.json": &fstest.MapFile{Data: []byte(`{not json`)},
	}
	n, err := reg.LoadManifests(fmap, "apps/manifests")
	if err == nil {
		t.Fatalf("loading a broken manifest succeeded (loaded %d), want error", n)
	}
	if _, ok := reg.Manifest("com.example.good"); ok {
		t.Fatal("partially loaded registry should not contain the good manifest")
	}

	// Valid directory loads and is visible.
	fmap2 := fstest.MapFS{
		"apps/manifests/com.example.good.json": &fstest.MapFile{
			Data: []byte(`{"id":"com.example.good","name":"Good","version":"0.2.0","entrypoint":"good"}`),
		},
	}
	n, err = reg.LoadManifests(fmap2, "apps/manifests")
	must(t, err)
	if n != 1 {
		t.Fatalf("loaded %d manifests, want 1", n)
	}
	m, ok := reg.Manifest("com.example.good")
	if !ok || m.Name != "Good" {
		t.Fatalf("manifest = %+v ok=%v", m, ok)
	}
}

func TestLaunchUnknownApp(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.Launch(context.Background(), "com.missing.app"); err == nil {
		t.Fatal("launching unknown app succeeded, want error")
	}
}

func TestLaunchRouteStopLifecycle(t *testing.T) {
	m, router := newTestManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proc, err := m.Launch(ctx, fakeManifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsRunning(fakeManifest.ID) {
		t.Fatal("app not marked running after launch")
	}

	// The app's route serves through the shared router.
	admin := ipc.WithCapabilities(context.Background(), security.AdminCapabilities())
	resp := router.Dispatch(admin, ipc.Request{ID: 1, Method: "app/com.test.fake/ping"})
	if !resp.OK {
		t.Fatalf("app route dispatch failed: %s", resp.Error)
	}

	// Single-instance policy.
	if _, err := m.Launch(ctx, fakeManifest.ID); err == nil {
		t.Fatal("duplicate launch succeeded, want error")
	}

	must(t, m.Stop(fakeManifest.ID, 2*time.Second))
	select {
	case <-proc.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit after Stop")
	}
	if m.IsRunning(fakeManifest.ID) {
		t.Fatal("app still marked running after stop")
	}
	resp = router.Dispatch(admin, ipc.Request{ID: 2, Method: "app/com.test.fake/ping"})
	if resp.OK {
		t.Fatal("app route still registered after exit")
	}
}

func TestListReflectsRunningState(t *testing.T) {
	m, _ := newTestManager(t)
	list := m.List()
	if len(list) != 1 || list[0].Running {
		t.Fatalf("list before launch = %+v", list)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must(t, func() error { _, err := m.Launch(ctx, fakeManifest.ID); return err }())
	list = m.List()
	if len(list) != 1 || !list[0].Running || list[0].PID == 0 {
		t.Fatalf("list after launch = %+v", list)
	}
}

// probe is test scaffolding, not a second demo app.
type probe struct {
	init func(*sdk.Context) error
	run  func(context.Context) error
	stop func(context.Context) error
}

func (p *probe) Init(c *sdk.Context) error { return p.init(c) }
func (p *probe) Run(c context.Context) error {
	if p.run != nil {
		return p.run(c)
	}
	<-c.Done()
	return nil
}
func (p *probe) Stop(c context.Context) error {
	if p.stop != nil {
		return p.stop(c)
	}
	return nil
}

func probeManager(t *testing.T, p *probe, caps ...string) (*Manager, *ipc.Router) {
	t.Helper()
	bus := events.NewBus()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := ipc.NewRouter()
	reg := NewRegistry()
	man := fakeManifest
	man.Permissions = caps
	must(t, reg.RegisterBuiltin(man, func() (Instance, error) { return p, nil }))
	return NewManager(reg, process.NewManager(bus, log), router, bus, log), router
}

func TestSDKScopesRunCallsAndHandlers(t *testing.T) {
	var appCtx *sdk.Context
	runCaps := make(chan []string, 1)
	p := &probe{init: func(c *sdk.Context) error {
		appCtx = c
		return c.Handle("delegate", func(ctx context.Context, raw json.RawMessage) (any, error) {
			return nil, c.Call(ctx, "fs/write", nil, nil)
		})
	}, run: func(ctx context.Context) error {
		runCaps <- ipc.Capabilities(ctx).List()
		<-ctx.Done()
		return nil
	}}
	m, r := probeManager(t, p, sdk.CapIPC)
	must(t, r.Handle("fs/write", func(c context.Context, _ ipc.Request) (any, error) {
		return nil, ipc.RequireCap(c, sdk.CapFileWrite)
	}))
	admin := ipc.WithCapabilities(context.Background(), security.AdminCapabilities())
	proc, err := m.Launch(admin, fakeManifest.ID)
	must(t, err)
	if got := <-runCaps; len(got) != 1 || got[0] != sdk.CapIPC {
		t.Fatalf("Run caps = %v", got)
	}
	if got := proc.Info().Caps; len(got) != 1 || got[0] != sdk.CapIPC {
		t.Fatalf("process caps = %v", got)
	}
	// Modifying the informational manifest cannot increase the captured grant.
	appCtx.Manifest.Permissions = append(appCtx.Manifest.Permissions, sdk.CapFileWrite)
	resp := r.Dispatch(admin, ipc.Request{Method: "app/com.test.fake/delegate"})
	if resp.OK || !strings.Contains(resp.Error, "fs.write") {
		t.Fatalf("deputy response = %+v", resp)
	}
	if err := appCtx.Call(admin, "fs/write", nil, nil); err == nil {
		t.Fatal("Call borrowed admin caps")
	}
	resp = r.Dispatch(context.Background(), ipc.Request{Method: "app/com.test.fake/delegate"})
	if resp.OK || !strings.Contains(resp.Error, "ipc") {
		t.Fatalf("unprivileged route response = %+v", resp)
	}
	if err := appCtx.Handle("late", func(context.Context, json.RawMessage) (any, error) { return nil, nil }); err == nil {
		t.Fatal("late route accepted")
	}
	must(t, m.Stop(fakeManifest.ID, 2*time.Second))
	if err := appCtx.Call(admin, "fs/write", nil, nil); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("post-stop call = %v", err)
	}
}

func TestInitFailureAndPanicRollback(t *testing.T) {
	for _, panicInit := range []bool{false, true} {
		t.Run(fmtBool(panicInit), func(t *testing.T) {
			stops := 0
			p := &probe{init: func(c *sdk.Context) error {
				must(t, c.Handle("partial", func(context.Context, json.RawMessage) (any, error) { return nil, nil }))
				if panicInit {
					panic("init broke")
				}
				return errors.New("init broke")
			}, stop: func(context.Context) error { stops++; return nil }}
			m, r := probeManager(t, p, sdk.CapIPC)
			if _, err := m.Launch(context.Background(), fakeManifest.ID); err == nil {
				t.Fatal("failed Init succeeded")
			}
			if stops != 1 || m.IsRunning(fakeManifest.ID) || len(r.Methods()) != 0 {
				t.Fatalf("rollback: stops=%d routes=%v", stops, r.Methods())
			}
		})
	}
}
func fmtBool(b bool) string {
	if b {
		return "panic"
	}
	return "error"
}

func TestRunPanicStopsAndRetracts(t *testing.T) {
	stopped := make(chan struct{})
	p := &probe{init: func(*sdk.Context) error { return nil }, run: func(context.Context) error { panic("run broke") }, stop: func(context.Context) error { close(stopped); return nil }}
	m, _ := probeManager(t, p, sdk.CapIPC)
	proc, err := m.Launch(context.Background(), fakeManifest.ID)
	must(t, err)
	select {
	case <-proc.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("panic hung process")
	}
	<-stopped
	if proc.Info().State != process.StateFailed || m.IsRunning(fakeManifest.ID) {
		t.Fatalf("panic state = %+v", proc.Info())
	}
}

func TestConcurrentLaunchReservesInstance(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p := &probe{init: func(*sdk.Context) error { close(entered); <-release; return nil }}
	m, _ := probeManager(t, p)
	result := make(chan error, 1)
	go func() { _, err := m.Launch(context.Background(), fakeManifest.ID); result <- err }()
	<-entered
	_, duplicateErr := m.Launch(context.Background(), fakeManifest.ID)
	close(release)
	must(t, <-result)
	if duplicateErr == nil {
		t.Fatal("concurrent launch accepted")
	}
	must(t, m.Stop(fakeManifest.ID, 2*time.Second))
}

func TestRouteConflictDoesNotRetractOtherOwner(t *testing.T) {
	p := &probe{init: func(c *sdk.Context) error {
		return c.Handle("owned", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	}}
	m, r := probeManager(t, p, sdk.CapIPC)
	must(t, r.Handle("app/com.test.fake/owned", func(context.Context, ipc.Request) (any, error) { return "owner", nil }))
	if _, err := m.Launch(context.Background(), fakeManifest.ID); err == nil {
		t.Fatal("conflict accepted")
	}
	if len(r.Methods()) != 1 {
		t.Fatal("rollback deleted another owner's route")
	}
}

func TestSDKDeclaredGrantAllowsAndCanceledCallDenies(t *testing.T) {
	var appCtx *sdk.Context
	p := &probe{init: func(c *sdk.Context) error { appCtx = c; return nil }}
	m, r := probeManager(t, p, sdk.CapIPC, sdk.CapFileWrite)
	writes := 0
	must(t, r.Handle("fs/write", func(ctx context.Context, _ ipc.Request) (any, error) {
		if err := ipc.RequireCap(ctx, sdk.CapFileWrite); err != nil {
			return nil, err
		}
		writes++
		return map[string]int{"written": 1}, nil
	}))
	_, err := m.Launch(context.Background(), fakeManifest.ID)
	must(t, err)
	var out struct {
		Written int `json:"written"`
	}
	must(t, appCtx.Call(context.Background(), "fs/write", nil, &out))
	if out.Written != 1 {
		t.Fatal("declared grant denied")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := appCtx.Call(ctx, "fs/write", nil, nil); err == nil {
		t.Fatal("canceled call performed write")
	}
	if writes != 1 {
		t.Fatal("canceled call reached handler")
	}
	must(t, m.Stop(fakeManifest.ID, 2*time.Second))
}

func TestNoIPCGrantAndNamespaceDenial(t *testing.T) {
	for _, name := range []string{"ping", "../sys", "sys/shutdown"} {
		p := &probe{init: func(c *sdk.Context) error {
			return c.Handle(name, func(context.Context, json.RawMessage) (any, error) { return nil, nil })
		}}
		m, _ := probeManager(t, p)
		if _, err := m.Launch(context.Background(), fakeManifest.ID); err == nil {
			t.Fatalf("route %q accepted without IPC/namespace", name)
		}
	}
}

func TestHandlersDrainBeforeStop(t *testing.T) {
	entered := make(chan struct{})
	returned := make(chan struct{})
	var appCtx *sdk.Context
	p := &probe{init: func(c *sdk.Context) error {
		appCtx = c
		return c.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
			close(entered)
			<-ctx.Done()
			close(returned)
			return nil, nil
		})
	}, stop: func(context.Context) error {
		select {
		case <-returned:
			return nil
		default:
			return errors.New("Stop ran before handler drained")
		}
	}}
	m, r := probeManager(t, p, sdk.CapIPC)
	_, err := m.Launch(context.Background(), fakeManifest.ID)
	must(t, err)
	response := make(chan ipc.Response, 1)
	go func() {
		response <- r.Dispatch(ipc.WithCapabilities(context.Background(), security.AdminCapabilities()), ipc.Request{Method: "app/com.test.fake/wait"})
	}()
	<-entered
	must(t, m.Stop(fakeManifest.ID, 2*time.Second))
	<-response
	if err := appCtx.Call(context.Background(), "sys/ping", nil, nil); err == nil {
		t.Fatal("stopped instance callable")
	}
}

func TestStopCleanupErrorIsVisible(t *testing.T) {
	p := &probe{init: func(*sdk.Context) error { return nil }, stop: func(context.Context) error { return errors.New("cleanup failed") }}
	m, _ := probeManager(t, p)
	_, err := m.Launch(context.Background(), fakeManifest.ID)
	must(t, err)
	if err := m.Stop(fakeManifest.ID, 2*time.Second); err == nil || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("Stop error = %v", err)
	}
}

func TestRegistryPermissionCopies(t *testing.T) {
	r := NewRegistry()
	man := fakeManifest
	man.Permissions = []string{sdk.CapIPC}
	must(t, r.RegisterBuiltin(man, func() (Instance, error) { return &fakeInstance{}, nil }))
	man.Permissions[0] = sdk.CapShutdown
	copy, _ := r.Manifest(man.ID)
	copy.Permissions[0] = sdk.CapShutdown
	got, _ := r.Manifest(man.ID)
	if got.Permissions[0] != sdk.CapIPC {
		t.Fatal("registry permissions mutated")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
