// Package process implements the environment's process model. An
// environment process is an explicit, tracked object with an ID, a name,
// a kind, a state, and a lifecycle — never an anonymous goroutine.
//
// Two kinds exist and are deliberately distinct:
//
//	inproc — application or service logic inside the runtime (a
//	         goroutine with a cancellable context). Logically isolated
//	         only: a panic or deadlock can affect the runtime.
//	child  — a real host child process (os/exec). Isolated by the host
//	         OS, with an exit code, killable from outside.
//
// Nothing in this package pretends a goroutine is an OS process.
package process

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"sync"
	"time"

	"gostalgia/internal/events"
)

type Kind string

const (
	KindInProc Kind = "inproc"
	KindChild  Kind = "child"
)

type State string

const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

// Spec describes a process to start.
type Spec struct {
	Name      string   `json:"name"`              // logical name (app id, child label)
	Kind      Kind     `json:"kind"`              // inproc or child
	SessionID string   `json:"session,omitempty"` // owning session
	User      string   `json:"user,omitempty"`    // owning user
	Caps      []string `json:"caps,omitempty"`    // granted capabilities
	Args      []string `json:"args,omitempty"`    // child only: program and arguments
	Dir       string   `json:"dir,omitempty"`     // child only: working directory
	Env       []string `json:"env,omitempty"`     // child only; nil inherits the host environment
}

// Info is a snapshot of a process's state.
type Info struct {
	ID        int32     `json:"id"`
	Name      string    `json:"name"`
	Kind      Kind      `json:"kind"`
	State     State     `json:"state"`
	SessionID string    `json:"session,omitempty"`
	User      string    `json:"user,omitempty"`
	Caps      []string  `json:"caps,omitempty"` // granted capabilities, from the spec
	StartedAt time.Time `json:"started_at,omitempty"`
	ExitedAt  time.Time `json:"exited_at,omitempty"`
	Err       string    `json:"error,omitempty"`
	ExitCode  int       `json:"exit_code,omitempty"`
}

// Event is published on every state transition.
type Event struct {
	ID    int32  `json:"id"`
	Name  string `json:"name"`
	Kind  Kind   `json:"kind"`
	State State  `json:"state"`
	Err   string `json:"error,omitempty"`
}

func (Event) Type() string { return "proc.state" }

// Process is one environment process.
type Process struct {
	mu     sync.Mutex
	info   Info
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	cmd    *exec.Cmd // child only
}

// ID returns the process ID.
func (p *Process) ID() int32 { return p.info.ID }

// Context returns the process context: canceled when the process is
// stopped. In-proc applications must honor it.
func (p *Process) Context() context.Context { return p.ctx }

// Done is closed when the process has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// Info returns a state snapshot.
func (p *Process) Info() Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

// Caps returns the capabilities granted to this process (from its spec).
func (p *Process) Caps() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info.Caps
}

func (p *Process) setState(state State, err error) {
	p.mu.Lock()
	p.info.State = state
	if err != nil {
		p.info.Err = err.Error()
	}
	p.mu.Unlock()
}

// Manager tracks all environment processes.
type Manager struct {
	mu    sync.Mutex
	next  int32
	procs map[int32]*Process
	bus   *events.Bus
	log   *slog.Logger
}

func NewManager(bus *events.Bus, log *slog.Logger) *Manager {
	return &Manager{
		next:  1,
		procs: map[int32]*Process{},
		bus:   bus,
		log:   log,
	}
}

func (m *Manager) alloc() int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	return m.next - 1
}

func (m *Manager) publish(p *Process) {
	info := p.Info()
	m.bus.Publish("process", Event{
		ID: info.ID, Name: info.Name, Kind: info.Kind, State: info.State, Err: info.Err,
	})
}

func (m *Manager) add(p *Process) {
	m.mu.Lock()
	m.procs[p.info.ID] = p
	m.mu.Unlock()
}

// StartInProc starts run as an environment process of kind "inproc". The
// process context is canceled by Stop; run must honor it.
func (m *Manager) StartInProc(ctx context.Context, spec Spec, run func(p *Process) error) (*Process, error) {
	if run == nil {
		return nil, errors.New("process: run function is required")
	}
	spec.Kind = KindInProc
	id := m.alloc()
	procCtx, cancel := context.WithCancel(ctx)
	p := &Process{done: make(chan struct{}), ctx: procCtx, cancel: cancel}
	p.info = Info{
		ID:        id,
		Name:      spec.Name,
		Kind:      spec.Kind,
		State:     StateStarting,
		SessionID: spec.SessionID,
		User:      spec.User,
		Caps:      spec.Caps,
		StartedAt: time.Now(),
	}
	m.add(p)
	m.publish(p) // starting
	p.setState(StateRunning, nil)
	m.publish(p)
	m.log.Info("process started", "pid", id, "name", spec.Name, "kind", spec.Kind)

	go func() {
		err := run(p)

		stopping := p.isStopping() // read before taking the lock
		p.mu.Lock()
		p.info.ExitedAt = time.Now()
		p.info.Err = ""
		state := StateStopped
		var finalErr error
		if err != nil && !stopping {
			state = StateFailed
			p.info.Err = err.Error()
			finalErr = err
		}
		p.info.State = state
		p.mu.Unlock()
		cancel() // release context resources
		m.publish(p)
		close(p.done)
		if finalErr != nil {
			m.log.Error("process failed", "pid", id, "name", spec.Name, "err", finalErr)
		} else {
			m.log.Info("process stopped", "pid", id, "name", spec.Name)
		}
	}()
	return p, nil
}

// StartChild starts spec.Args as a real host child process. The child is
// killed when its context is canceled (i.e. by Stop). Its combined output
// is captured and returned via Info in later milestones; for now exit
// status is tracked.
func (m *Manager) StartChild(ctx context.Context, spec Spec) (*Process, error) {
	if len(spec.Args) == 0 {
		return nil, errors.New("process: child spec requires args")
	}
	spec.Kind = KindChild
	id := m.alloc()
	procCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(procCtx, spec.Args[0], spec.Args[1:]...)
	cmd.Dir = spec.Dir
	if spec.Env != nil {
		cmd.Env = spec.Env // nil inherits the host environment
	}

	p := &Process{done: make(chan struct{}), ctx: procCtx, cancel: cancel, cmd: cmd}
	p.info = Info{
		ID:        id,
		Name:      spec.Name,
		Kind:      spec.Kind,
		State:     StateStarting,
		SessionID: spec.SessionID,
		User:      spec.User,
		Caps:      spec.Caps,
		StartedAt: time.Now(),
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("process: start %s: %w", spec.Args[0], err)
	}
	m.add(p)
	m.publish(p) // starting
	p.setState(StateRunning, nil)
	m.publish(p)
	m.log.Info("child process started", "pid", id, "name", spec.Name)

	go func() {
		err := cmd.Wait()

		stopping := p.isStopping() // read before taking the lock
		p.mu.Lock()
		p.info.ExitedAt = time.Now()
		p.info.Err = ""
		if cmd.ProcessState != nil {
			p.info.ExitCode = cmd.ProcessState.ExitCode()
		}
		state := StateStopped
		if err != nil && !stopping {
			state = StateFailed
			if cmd.ProcessState != nil {
				p.info.Err = fmt.Sprintf("exit code %d: %v", cmd.ProcessState.ExitCode(), err)
			} else {
				p.info.Err = err.Error()
			}
		}
		p.info.State = state
		p.mu.Unlock()
		cancel()
		m.publish(p)
		close(p.done)
		m.log.Info("child process exited", "pid", id, "name", spec.Name, "exit_code", p.info.ExitCode, "state", p.info.State)
	}()
	return p, nil
}

func (p *Process) isStopping() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info.State == StateStopping
}

// Stop asks a process to stop and waits up to timeout for it to exit.
func (m *Manager) Stop(id int32, timeout time.Duration) error {
	m.mu.Lock()
	p, ok := m.procs[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("process: no such process %d", id)
	}
	switch p.Info().State {
	case StateStopped, StateFailed, StateStopping:
		select {
		case <-p.done:
			return nil
		case <-time.After(timeout):
			return fmt.Errorf("process: %d is stopping but has not exited within %s", id, timeout)
		}
	}
	p.setState(StateStopping, nil)
	m.publish(p)
	p.cancel()
	select {
	case <-p.done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("process: %d (%s) did not stop within %s", id, p.Info().Name, timeout)
	}
}

// StopAll stops every live process, best effort. Used at shutdown.
func (m *Manager) StopAll(timeout time.Duration) {
	for _, info := range m.List() {
		if info.State != StateRunning && info.State != StateStarting {
			continue
		}
		if err := m.Stop(info.ID, timeout); err != nil {
			m.log.Warn("process shutdown problem", "pid", info.ID, "err", err)
		}
	}
}

// Get returns a process by ID.
func (m *Manager) Get(id int32) (*Process, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[id]
	return p, ok
}

// List returns snapshots of all known processes, sorted by ID. Exited
// processes remain listed until reaping (future milestone).
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, 0, len(m.procs))
	for _, p := range m.procs {
		out = append(out, p.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Count returns the number of live (running or starting) processes.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.procs {
		switch p.Info().State {
		case StateRunning, StateStarting:
			n++
		}
	}
	return n
}
