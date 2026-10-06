// Package app implements the standard-library-only application registry and
// lifecycle manager. Application-facing contracts live in gostalgia/sdk.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"sync"
	"time"

	"gostalgia/internal/events"
	"gostalgia/internal/ipc"
	"gostalgia/internal/process"
	"gostalgia/internal/security"
	"gostalgia/sdk"
)

type Manifest = sdk.Manifest
type Instance = sdk.Instance
type Factory = sdk.Factory

// Event is published when an application launches or exits.
type Event struct {
	ID    string `json:"id"`
	PID   int32  `json:"pid"`
	State string `json:"state"`
	Err   string `json:"error,omitempty"`
}

func (Event) Type() string { return "app.state" }

type Status struct {
	Manifest Manifest `json:"manifest"`
	Running  bool     `json:"running"`
	PID      int32    `json:"pid,omitempty"`
}

// Registry holds compiled-in factories and installed manifests. JSON alone
// cannot install executable code; builtin registrations win over disk copies.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
	manifests map[string]Manifest
}

func NewRegistry() *Registry {
	return &Registry{factories: map[string]Factory{}, manifests: map[string]Manifest{}}
}

func cloneManifest(m Manifest) Manifest {
	m.Permissions = append([]string(nil), m.Permissions...)
	return m
}

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
	r.manifests[m.ID] = cloneManifest(m)
	r.factories[m.Entrypoint] = f
	return nil
}

// LoadManifests validates the entire directory before adding any documents.
func (r *Registry) LoadManifests(fsys fs.FS, dir string) (int, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return 0, fmt.Errorf("app: read manifests from %s: %w", dir, err)
	}
	var pending []Manifest
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return 0, fmt.Errorf("app: read %s/%s: %w", dir, e.Name(), err)
		}
		m, err := sdk.ParseManifest(b)
		if err != nil {
			return 0, fmt.Errorf("app: %s/%s: %w", dir, e.Name(), err)
		}
		pending = append(pending, m)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	loaded := 0
	for _, m := range pending {
		if _, exists := r.manifests[m.ID]; !exists {
			r.manifests[m.ID] = cloneManifest(m)
			loaded++
		}
	}
	return loaded, nil
}

func (r *Registry) Manifest(id string) (Manifest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.manifests[id]
	return cloneManifest(m), ok
}

func (r *Registry) Manifests() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Manifest, 0, len(r.manifests))
	for _, m := range r.manifests {
		out = append(out, cloneManifest(m))
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

// Manager reserves the app id before invoking user code. No manager lock is
// held across application callbacks or synchronous bus publications.
type Manager struct {
	reg     *Registry
	procs   *process.Manager
	router  *ipc.Router
	bus     *events.Bus
	tokens  *security.TokenStore
	log     *slog.Logger
	mu      sync.Mutex
	running map[string]*runningApp
}

type runningApp struct {
	pid        int32 // zero while initializing
	token      string
	cleanupErr error // written before process Done closes
}

func NewManager(reg *Registry, procs *process.Manager, router *ipc.Router, bus *events.Bus, log *slog.Logger) *Manager {
	return &Manager{
		reg:     reg,
		procs:   procs,
		router:  router,
		bus:     bus,
		tokens:  security.NewTokenStore(),
		log:     log,
		running: map[string]*runningApp{},
	}
}

// SetTokenStore configures the token store used for application credentials.
func (m *Manager) SetTokenStore(tokens *security.TokenStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens = tokens
}

// TokenStore returns the token store.
func (m *Manager) TokenStore() *security.TokenStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens
}

// AppToken returns the launch-bound credential issued for a running app.
func (m *Manager) AppToken(id string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ra, ok := m.running[id]
	if !ok || ra.token == "" {
		return "", false
	}
	return ra.token, true
}

// invoke turns lifecycle panics into process/launch errors, never success.
func invoke(phase string, fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("app: %s panicked: %v", phase, p)
		}
	}()
	return fn()
}

// Launch runs Factory -> Init -> Run -> Stop. ctx owns the instance lifetime.
// IPC launchers must supply a runtime-lifetime context, not a request deadline.
func (m *Manager) Launch(ctx context.Context, id string) (*process.Process, error) {
	man, ok := m.reg.Manifest(id)
	if !ok {
		return nil, fmt.Errorf("app: unknown application %q", id)
	}
	factory, ok := m.reg.factory(man.Entrypoint)
	if !ok {
		return nil, fmt.Errorf("app: no factory for entrypoint %q", man.Entrypoint)
	}
	ra := &runningApp{}
	m.mu.Lock()
	if existing, dup := m.running[id]; dup {
		pid := existing.pid
		m.mu.Unlock()
		return nil, fmt.Errorf("app: %s is already running or initializing (pid %d)", id, pid)
	}
	m.running[id] = ra
	m.mu.Unlock()
	forget := func() {
		m.mu.Lock()
		delete(m.running, id)
		m.mu.Unlock()
	}

	var inst Instance
	if err := invoke("factory", func() (err error) { inst, err = factory(); return err }); err != nil {
		forget()
		return nil, fmt.Errorf("app: create %s: %w", id, err)
	}
	if inst == nil {
		forget()
		return nil, fmt.Errorf("app: %s: factory returned nil", id)
	}

	var appToken string
	caps := append([]string(nil), man.Permissions...)
	if m.tokens != nil {
		var tokErr error
		appToken, tokErr = m.tokens.IssueAppToken(id, 0, "", security.User{Name: "guest"}, caps...)
		if tokErr != nil {
			forget()
			return nil, fmt.Errorf("app: issue token for %s: %w", id, tokErr)
		}
		ra.token = appToken
	}

	life, cancel := context.WithCancel(ctx)
	base := "app/" + id + "/"
	var mu sync.Mutex
	initializing, active := true, true
	registered := false
	routes := map[string]ipc.Handler{}
	var inflight sync.WaitGroup

	// Always replace incoming caps and principal: an operator calling this app
	// must not lend the app operator privileges (the confused-deputy boundary).
	scope := func(parent context.Context) (context.Context, func()) {
		c, stop := context.WithCancel(parent)
		detach := context.AfterFunc(life, stop)
		c = ipc.WithCapabilities(c, security.NewCapabilities(caps...))
		m.mu.Lock()
		curPID := ra.pid
		m.mu.Unlock()
		c = ipc.WithPrincipal(c, security.AppPrincipal(id, curPID, "", security.User{Name: "guest"}))
		return c, func() { detach(); stop() }
	}
	call := func(parent context.Context, method string, params, out any) error {
		if err := life.Err(); err != nil {
			return fmt.Errorf("app: instance stopped: %w", err)
		}
		c, stop := scope(parent)
		defer stop()
		if err := ipc.RequireCap(c, security.CapIPC); err != nil {
			return err
		}
		raw, err := ipc.Encode(params)
		if err != nil {
			return err
		}
		if err := c.Err(); err != nil {
			return err
		}
		resp := m.router.Dispatch(c, ipc.Request{Method: method, Params: raw})
		if !resp.OK {
			return errors.New(resp.Error)
		}
		if out != nil {
			return json.Unmarshal(resp.Data, out)
		}
		return nil
	}
	handle := func(name string, h sdk.Handler) error {
		mu.Lock()
		defer mu.Unlock()
		if !initializing {
			return fmt.Errorf("app: routes may only be declared during Init")
		}
		if !security.NewCapabilities(caps...).Has(security.CapIPC) {
			return fmt.Errorf("permission denied: missing capability %q", security.CapIPC)
		}
		method := base + name
		if _, dup := routes[method]; dup {
			return fmt.Errorf("app: duplicate route %q", name)
		}
		routes[method] = func(parent context.Context, req ipc.Request) (any, error) {
			if err := ipc.RequireCap(parent, security.CapIPC); err != nil {
				return nil, err
			}
			mu.Lock()
			if !active {
				mu.Unlock()
				return nil, fmt.Errorf("app: instance stopped")
			}
			inflight.Add(1)
			mu.Unlock()
			defer inflight.Done()
			c, stop := scope(parent)
			defer stop()
			if err := c.Err(); err != nil {
				return nil, err
			}
			return h(c, req.Params)
		}
		return nil
	}
	cleanup := func() error {
		mu.Lock()
		active, initializing = false, false
		mu.Unlock()
		cancel()
		if m.tokens != nil && ra.token != "" {
			m.tokens.Revoke(ra.token)
		}
		if registered {
			for method := range routes {
				m.router.Unhandle(method)
			}
		}
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		drained := make(chan struct{})
		go func() { inflight.Wait(); close(drained) }()
		var drainErr error
		select {
		case <-drained:
		case <-stopCtx.Done():
			drainErr = fmt.Errorf("app: handlers did not drain: %w", stopCtx.Err())
		}
		return errors.Join(drainErr, invoke("Stop", func() error { return inst.Stop(stopCtx) }))
	}
	lc := sdk.NewContext(cloneManifest(man), m.log.With("app", id), call, handle)
	err := invoke("Init", func() error { return inst.Init(lc) })
	mu.Lock()
	initializing = false
	mu.Unlock()
	if err == nil {
		err = life.Err()
	}
	if err == nil {
		err = m.router.HandleBatch(routes)
	}
	if err != nil {
		stopErr := cleanup()
		forget()
		return nil, fmt.Errorf("app: init %s: %w", id, errors.Join(err, stopErr))
	}

	registered = true
	ready := make(chan struct{})
	proc, err := m.procs.StartInProc(life, process.Spec{Name: id, Caps: caps}, func(p *process.Process) (runErr error) {
		<-ready
		defer func() {
			ra.cleanupErr = cleanup()
			runErr = errors.Join(runErr, ra.cleanupErr)
			forget()
			msg := ""
			if runErr != nil {
				msg = runErr.Error()
			}
			m.bus.Publish("app", Event{ID: id, PID: p.ID(), State: "exited", Err: msg})
		}()
		return invoke("Run", func() error { return inst.Run(p.Context()) })
	})
	if err != nil {
		stopErr := cleanup()
		forget()
		return nil, errors.Join(err, stopErr)
	}
	m.mu.Lock()
	ra.pid = proc.ID()
	m.mu.Unlock()
	if m.tokens != nil && appToken != "" {
		m.tokens.BindProcess(appToken, proc.ID())
	}
	// Release Run before publishing: event subscribers may synchronously Stop.
	close(ready)
	m.bus.Publish("app", Event{ID: id, PID: proc.ID(), State: "launched"})
	m.log.Info("application launched", "app", id, "pid", proc.ID())
	return proc, nil
}

// Stop waits for Run, handler draining, Stop, and route/state retraction.
func (m *Manager) Stop(id string, timeout time.Duration) error {
	m.mu.Lock()
	ra, ok := m.running[id]
	pid := int32(0)
	if ok {
		pid = ra.pid
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("app: %s is not running", id)
	}
	if pid == 0 {
		return fmt.Errorf("app: %s is still initializing", id)
	}
	if err := m.procs.Stop(pid, timeout); err != nil {
		return err
	}
	return ra.cleanupErr
}

func (m *Manager) IsRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[id]
	return ok
}

func (m *Manager) Running() map[string]int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int32, len(m.running))
	for id, ra := range m.running {
		out[id] = ra.pid
	}
	return out
}

func (m *Manager) List() []Status {
	running := m.Running()
	out := make([]Status, 0)
	for _, man := range m.reg.Manifests() {
		pid, ok := running[man.ID]
		out = append(out, Status{Manifest: man, Running: ok, PID: pid})
	}
	return out
}
