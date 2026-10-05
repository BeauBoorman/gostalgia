// Package app implements the environment's application model: manifests
// (what an application is), a registry (what is installed), and a manager
// (how applications launch, run, and stop inside the environment).
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"sync"
	"time"

	"gostalgia/internal/events"
	"gostalgia/internal/ipc"
	"gostalgia/internal/process"
	"gostalgia/internal/vfs"
)

// Manifest declares an application. It is the environment-facing
// description: identity, version, the entrypoint (a registered factory
// for builtin apps, a binary name for out-of-proc apps later), and the
// permissions the application requires.
type Manifest struct {
	ID          string   `json:"id"`         // reverse-DNS: com.gostalgia.echo
	Name        string   `json:"name"`       // display name
	Version     string   `json:"version"`    // semver-ish: 0.1.0
	Entrypoint  string   `json:"entrypoint"` // registered factory name
	Permissions []string `json:"permissions,omitempty"`
	Description string   `json:"description,omitempty"`
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z0-9-]+)+$`)

// Validate checks the manifest's required structure.
func (m Manifest) Validate() error {
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("app: manifest id %q is not a reverse-DNS name like com.example.editor", m.ID)
	}
	if m.Name == "" {
		return fmt.Errorf("app: manifest %s: name is required", m.ID)
	}
	if m.Version == "" {
		return fmt.Errorf("app: manifest %s: version is required", m.ID)
	}
	if m.Entrypoint == "" {
		return fmt.Errorf("app: manifest %s: entrypoint is required", m.ID)
	}
	return nil
}

// Instance is a running application. Run must return promptly when ctx is
// canceled (ctx is the owning process's context).
type Instance interface {
	Run(ctx context.Context, p *process.Process) error
	// RegisterRoutes installs the application's IPC methods under base
	// ("app/<id>"), e.g. base+"/echo".
	RegisterRoutes(r *ipc.Router, base string) error
}

// LaunchContext carries the wiring an application factory may use.
type LaunchContext struct {
	Manifest Manifest
	Log      *slog.Logger
	VFS      vfs.FS
	Router   *ipc.Router
	Procs    *process.Manager
	Events   *events.Bus
}

// Factory creates an application instance for launch.
type Factory func(lc LaunchContext) (Instance, error)

// Event is published when an application launches or exits.
type Event struct {
	ID    string `json:"id"`
	PID   int32  `json:"pid"`
	State string `json:"state"` // "launched" or "exited"
	Err   string `json:"error,omitempty"`
}

func (Event) Type() string { return "app.state" }

// Status is one installed application and whether it is running.
type Status struct {
	Manifest Manifest `json:"manifest"`
	Running  bool     `json:"running"`
	PID      int32    `json:"pid,omitempty"`
}

// Registry holds builtin factories and known manifests. Manifests also
// arrive as JSON documents in the VFS (/apps/manifests); factories are
// always builtin for now — out-of-proc entrypoints are a later milestone.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
	manifests map[string]Manifest
}

func NewRegistry() *Registry {
	return &Registry{
		factories: map[string]Factory{},
		manifests: map[string]Manifest{},
	}
}

// RegisterBuiltin registers a manifest with its factory.
func (r *Registry) RegisterBuiltin(m Manifest, f Factory) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("app: %s: factory is required", m.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.manifests[m.ID]; dup {
		return fmt.Errorf("app: %s is already registered", m.ID)
	}
	if _, dup := r.factories[m.Entrypoint]; dup {
		return fmt.Errorf("app: entrypoint %q is already registered", m.Entrypoint)
	}
	r.manifests[m.ID] = m
	r.factories[m.Entrypoint] = f
	return nil
}

// LoadManifests reads manifest JSON documents from dir in fsys and adds
// them to the registry. Manifests whose id is already known are skipped
// (builtin registrations win). A malformed document is an error.
func (r *Registry) LoadManifests(fsys fs.FS, dir string) (int, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return 0, fmt.Errorf("app: read manifests from %s: %w", dir, err)
	}
	loaded := 0
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return loaded, fmt.Errorf("app: read %s/%s: %w", dir, e.Name(), err)
		}
		var m Manifest
		if err := json.Unmarshal(b, &m); err != nil {
			return loaded, fmt.Errorf("app: parse %s/%s: %w", dir, e.Name(), err)
		}
		if err := m.Validate(); err != nil {
			return loaded, fmt.Errorf("app: %s/%s: %w", dir, e.Name(), err)
		}
		r.mu.Lock()
		if _, exists := r.manifests[m.ID]; !exists {
			r.manifests[m.ID] = m
			loaded++
		}
		r.mu.Unlock()
	}
	return loaded, nil
}

// Manifest returns the manifest for id.
func (r *Registry) Manifest(id string) (Manifest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.manifests[id]
	return m, ok
}

// Manifests lists all known manifests, sorted by id.
func (r *Registry) Manifests() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Manifest, 0, len(r.manifests))
	for _, m := range r.manifests {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) factory(entrypoint string) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[entrypoint]
	return f, ok
}

// Manager launches and tracks application instances. For now one instance
// per application id is allowed (single-instance policy); duplicates are
// rejected with the running pid.
type Manager struct {
	reg    *Registry
	procs  *process.Manager
	router *ipc.Router
	bus    *events.Bus
	log    *slog.Logger

	mu      sync.Mutex
	running map[string]*runningApp
}

// runningApp tracks one live instance. Cleanup runs exactly once whether
// the app is stopped deliberately or exits on its own, and Stop waits for
// it — callers must never observe stale routes or running state.
type runningApp struct {
	pid  int32
	once sync.Once
	done func()
}

func NewManager(reg *Registry, procs *process.Manager, router *ipc.Router, bus *events.Bus, log *slog.Logger) *Manager {
	return &Manager{
		reg:     reg,
		procs:   procs,
		router:  router,
		bus:     bus,
		log:     log,
		running: map[string]*runningApp{},
	}
}

// Launch starts application id inside the environment.
func (m *Manager) Launch(ctx context.Context, id string) (*process.Process, error) {
	man, ok := m.reg.Manifest(id)
	if !ok {
		return nil, fmt.Errorf("app: unknown application %q", id)
	}
	factory, ok := m.reg.factory(man.Entrypoint)
	if !ok {
		return nil, fmt.Errorf("app: no factory for entrypoint %q", man.Entrypoint)
	}
	m.mu.Lock()
	if ra, dup := m.running[id]; dup {
		m.mu.Unlock()
		return nil, fmt.Errorf("app: %s is already running (pid %d)", id, ra.pid)
	}
	m.mu.Unlock()

	inst, err := factory(LaunchContext{
		Manifest: man,
		Log:      m.log.With("app", id),
		VFS:      nil, // per-app scoped views arrive with permission enforcement
		Router:   m.router,
		Procs:    m.procs,
		Events:   m.bus,
	})
	if err != nil {
		return nil, fmt.Errorf("app: create %s: %w", id, err)
	}

	base := "app/" + man.ID
	if err := inst.RegisterRoutes(m.router, base); err != nil {
		return nil, fmt.Errorf("app: %s: register routes: %w", id, err)
	}

	proc, err := m.procs.StartInProc(ctx, process.Spec{
		Name: man.ID,
		Kind: process.KindInProc,
		Caps: man.Permissions,
	}, func(p *process.Process) error {
		return inst.Run(p.Context(), p)
	})
	if err != nil {
		m.router.UnhandlePrefix(base)
		return nil, fmt.Errorf("app: start %s: %w", id, err)
	}

	ra := &runningApp{pid: proc.ID()}
	ra.done = func() {
		ra.once.Do(func() {
			m.router.UnhandlePrefix(base)
			m.mu.Lock()
			delete(m.running, id)
			m.mu.Unlock()
			info := proc.Info()
			m.bus.Publish("app", Event{ID: id, PID: info.ID, State: "exited", Err: info.Err})
			m.log.Info("application exited", "app", id, "pid", info.ID, "state", info.State, "err", info.Err)
		})
	}
	m.mu.Lock()
	m.running[id] = ra
	m.mu.Unlock()
	m.bus.Publish("app", Event{ID: id, PID: proc.ID(), State: "launched"})
	m.log.Info("application launched", "app", id, "pid", proc.ID(), "version", man.Version)

	go func() {
		<-proc.Done()
		ra.done()
	}()
	return proc, nil
}

// Stop stops a running application and waits until its routes and state
// have been retracted.
func (m *Manager) Stop(id string, timeout time.Duration) error {
	m.mu.Lock()
	ra, ok := m.running[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("app: %s is not running", id)
	}
	if err := m.procs.Stop(ra.pid, timeout); err != nil {
		return err
	}
	ra.done()
	return nil
}

// IsRunning reports whether an instance of id is running.
func (m *Manager) IsRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[id]
	return ok
}

// Running returns a copy of the app-id -> pid map.
func (m *Manager) Running() map[string]int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int32, len(m.running))
	for id, ra := range m.running {
		out[id] = ra.pid
	}
	return out
}

// List reports every known application with its running state.
func (m *Manager) List() []Status {
	running := m.Running()
	out := make([]Status, 0)
	for _, man := range m.reg.Manifests() {
		st := Status{Manifest: man, Running: false}
		if pid, ok := running[man.ID]; ok {
			st.Running = true
			st.PID = pid
		}
		out = append(out, st)
	}
	return out
}
