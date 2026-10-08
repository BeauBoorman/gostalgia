package apps

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gostalgia/apps/calculator"
	"gostalgia/apps/compendium"
	"gostalgia/apps/dogcalc"
	"gostalgia/apps/echo"
	"gostalgia/apps/files"
	"gostalgia/apps/markview"
	"gostalgia/apps/musictoy"
	"gostalgia/apps/notes"
	"gostalgia/apps/petwatch"
	"gostalgia/apps/pomodoro"
	"gostalgia/apps/rss"
	"gostalgia/apps/settings"
	"gostalgia/apps/todo"
	"gostalgia/internal/app"
	"gostalgia/internal/events"
	"gostalgia/internal/ipc"
	"gostalgia/internal/process"
	"gostalgia/internal/vfs"
)

// seededEnv builds a VFS like the runtime does at boot: host-backed root
// with the layout InitRoot would create, plus the /tmp memfs mount.
func seededEnv(t *testing.T) *vfs.VFS {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "vfs"), 0o755); err != nil {
		t.Fatal(err)
	}
	hostFS, err := vfs.NewHost(filepath.Join(root, "vfs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hostFS.Close() })
	env := vfs.New(hostFS)
	if err := env.Mount("/tmp", vfs.NewMem()); err != nil {
		t.Fatal(err)
	}
	if err := env.MkdirAll("/apps/manifests"); err != nil {
		t.Fatal(err)
	}
	return env
}

// TestSeedManifestsWritesIdempotently: every builtin manifest lands in
// /apps/manifests as data, parses back to the same manifest, and seeding
// twice does not duplicate or error.
func TestSeedManifestsWritesIdempotently(t *testing.T) {
	env := seededEnv(t)
	if err := app.SeedManifests(env, Manifests()); err != nil {
		t.Fatalf("first SeedManifests: %v", err)
	}
	if err := app.SeedManifests(env, Manifests()); err != nil {
		t.Fatalf("second SeedManifests (idempotency): %v", err)
	}

	for _, id := range []string{echo.ID, notes.ID, files.ID, settings.ID, calculator.ID, compendium.ID, dogcalc.ID, petwatch.ID, pomodoro.ID, todo.ID, musictoy.ID, rss.ID, markview.ID} {
		data, err := env.ReadFile("/apps/manifests/" + id + ".json")
		if err != nil {
			t.Fatalf("seeded manifest readable for %s: %v", id, err)
		}
		var got app.Manifest
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("seeded manifest parses for %s: %v", id, err)
		}
	}

	entries, err := env.ReadDir("/apps/manifests")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(Manifests()) {
		t.Errorf("manifest dir has %d entries after double seed, want %d", len(entries), len(Manifests()))
	}
}

// TestRegisterMakesFactoryAvailable: registration wires each builtin's
// manifest to its factory well enough to launch it, and the seeded
// manifest loads back into a fresh registry as the same application.
func TestRegisterMakesFactoryAvailable(t *testing.T) {
	registry := app.NewRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	// Launch through the public app model: the factory must produce a
	// runnable instance supervised by the process manager.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	router := ipc.NewRouter()
	procs := process.NewManager(bus, log)
	mgr := app.NewManager(registry, procs, router, bus, log)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, err := mgr.Launch(ctx, echo.ID)
	if err != nil {
		t.Fatalf("launch %s after Register: %v", echo.ID, err)
	}
	if !mgr.IsRunning(echo.ID) {
		t.Fatal("app not marked running after launch")
	}
	if err := mgr.Stop(echo.ID, 2*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case <-proc.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit after Stop")
	}

	// The seeded document is loadable and agrees with the builtin.
	env := seededEnv(t)
	if err := app.SeedManifests(env, Manifests()); err != nil {
		t.Fatal(err)
	}
	fresh := app.NewRegistry()
	n, err := fresh.LoadManifests(env, "apps/manifests")
	if err != nil {
		t.Fatal(err)
	}
	if n != len(Manifests()) {
		t.Fatalf("loaded %d manifests, want %d", n, len(Manifests()))
	}
	for _, expected := range Manifests() {
		m, ok := fresh.Manifest(expected.ID)
		if !ok || !reflect.DeepEqual(m, expected) {
			t.Errorf("loaded manifest %s = %+v ok=%v, want %+v", expected.ID, m, ok, expected)
		}
	}
}
