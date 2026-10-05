package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gostalgia/internal/events"
	"gostalgia/internal/ipc"
	"gostalgia/internal/service"
	"gostalgia/platform"
)

// bootTestContext carries the minimum wiring the ipc service needs.
func bootTestContext(t *testing.T) *service.Context {
	t.Helper()
	return &service.Context{
		Root:     t.TempDir(),
		Version:  "test",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Router:   ipc.NewRouter(),
		Token:    "test-token",
		BootedAt: time.Now(),
	}
}

// TestIPCStartFailureUnwindsListener: if writing runtime.json fails, Start
// must close the listener and serve goroutine it already opened — the
// framework's rollback never Stops a service whose Start failed, so
// nothing else would.
func TestIPCStartFailureUnwindsListener(t *testing.T) {
	sctx := bootTestContext(t)
	svc := NewIPC()
	if err := svc.Init(sctx); err != nil {
		t.Fatal(err)
	}
	// Force writeRuntimeFile to fail: a directory sits at the path.
	if err := os.MkdirAll(filepath.Join(sctx.Root, "runtime.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded despite runtime.json being a directory")
	}
	if svc.server != nil || svc.listener != nil {
		t.Fatal("listener or server left live after failed Start")
	}
	if svc.endpoint == "" {
		t.Fatal("endpoint not recorded for diagnostics")
	}
	if conn, err := platform.DialIPC(svc.endpoint); err == nil {
		conn.Close()
		t.Fatal("endpoint still accepting connections after failed Start")
	}
}

// failingStartService refuses to start. Its name sorts after "ipc" under
// both start orders (registration and the old name-based one), so the ipc
// service reaches running first either way.
type failingStartService struct{}

func (f *failingStartService) Name() string                { return "zzz-fail" }
func (f *failingStartService) Depends() []string           { return nil }
func (f *failingStartService) Init(*service.Context) error { return nil }
func (f *failingStartService) Start(context.Context) error { return errors.New("boom") }
func (f *failingStartService) Stop(context.Context) error  { return nil }

// TestStartAllFailureRemovesRuntimeFile: when StartAll fails after the ipc
// service started, the rollback must leave no runtime.json behind — it is
// removed by the ipc service's own Stop during rollback. A leftover file
// would point gctl and the next boot at a socket nobody serves.
func TestStartAllFailureRemovesRuntimeFile(t *testing.T) {
	sctx := bootTestContext(t)
	sm := service.NewManager(sctx, events.NewBus(), sctx.Log)
	sctx.Services = sm
	for _, svc := range []service.Service{NewIPC(), &failingStartService{}} {
		if err := sm.Register(svc); err != nil {
			t.Fatal(err)
		}
	}
	if err := sm.StartAll(context.Background()); err == nil {
		t.Fatal("StartAll succeeded despite a failing service, want error")
	}
	if _, err := os.Stat(filepath.Join(sctx.Root, "runtime.json")); !os.IsNotExist(err) {
		t.Fatalf("runtime.json left behind after StartAll failure (stat err: %v)", err)
	}
}
