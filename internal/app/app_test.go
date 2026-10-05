package app

import (
	"context"
	"io"
	"log/slog"
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

func (f *fakeInstance) Run(ctx context.Context, p *process.Process) error {
	<-ctx.Done()
	return nil
}

func (f *fakeInstance) RegisterRoutes(r *ipc.Router, base string) error {
	return r.Handle(base+"/ping", func(ctx context.Context, req ipc.Request) (any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.pings++
		return map[string]int{"pings": f.pings}, nil
	})
}

var fakeManifest = Manifest{
	ID:         "com.test.fake",
	Name:       "Fake",
	Version:    "1.0.0",
	Entrypoint: "fake",
}

func newTestManager(t *testing.T) (*Manager, *ipc.Router) {
	t.Helper()
	bus := events.NewBus()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := ipc.NewRouter()
	procs := process.NewManager(bus, log)
	reg := NewRegistry()
	must(t, reg.RegisterBuiltin(fakeManifest, func(lc LaunchContext) (Instance, error) {
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
	must(t, reg.RegisterBuiltin(good, func(lc LaunchContext) (Instance, error) { return &fakeInstance{}, nil }))
	if err := reg.RegisterBuiltin(good, func(lc LaunchContext) (Instance, error) { return &fakeInstance{}, nil }); err == nil {
		t.Fatal("duplicate id accepted")
	}
	dupEntry := fakeManifest
	dupEntry.ID = "com.test.other"
	if err := reg.RegisterBuiltin(dupEntry, func(lc LaunchContext) (Instance, error) { return &fakeInstance{}, nil }); err == nil {
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

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
