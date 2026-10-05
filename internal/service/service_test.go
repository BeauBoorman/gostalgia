package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"gostalgia/internal/events"
)

// fakeService records its lifecycle transitions into a shared slice.
type fakeService struct {
	name     string
	deps     []string
	initErr  error
	startErr error
	stopErr  error

	mu     sync.Mutex
	events *[]string
}

func (f *fakeService) Name() string        { return f.name }
func (f *fakeService) Depends() []string   { return f.deps }
func (f *fakeService) Init(*Context) error { f.record("init"); return f.initErr }
func (f *fakeService) Start(context.Context) error {
	f.record("start")
	return f.startErr
}
func (f *fakeService) Stop(context.Context) error {
	f.record("stop")
	return f.stopErr
}

func (f *fakeService) record(op string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.events = append(*f.events, op+":"+f.name)
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewManager(&Context{}, events.NewBus(), log)
}

func TestStartAllOrdersByDependency(t *testing.T) {
	var seq []string
	m := newTestManager(t)
	mustRegister(t, m,
		&fakeService{name: "c", deps: []string{"b"}, events: &seq},
		&fakeService{name: "a", events: &seq},
		&fakeService{name: "b", deps: []string{"a"}, events: &seq},
	)
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"init:a", "start:a", "init:b", "start:b", "init:c", "start:c"}
	if len(seq) != len(want) {
		t.Fatalf("sequence = %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("sequence = %v, want %v", seq, want)
		}
	}
}

func TestStopAllReversesStartOrder(t *testing.T) {
	var seq []string
	m := newTestManager(t)
	mustRegister(t, m,
		&fakeService{name: "a", events: &seq},
		&fakeService{name: "b", deps: []string{"a"}, events: &seq},
	)
	mustStart(t, m)
	if err := m.StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"init:a", "start:a", "init:b", "start:b", "stop:b", "stop:a"}
	if len(seq) != len(want) {
		t.Fatalf("sequence = %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("sequence = %v, want %v", seq, want)
		}
	}
}

func TestStartFailureRollsBack(t *testing.T) {
	var seq []string
	m := newTestManager(t)
	mustRegister(t, m,
		&fakeService{name: "a", events: &seq},
		&fakeService{name: "b", deps: []string{"a"}, events: &seq, startErr: errors.New("boom")},
		&fakeService{name: "c", deps: []string{"b"}, events: &seq},
	)
	if err := m.StartAll(context.Background()); err == nil {
		t.Fatal("StartAll succeeded, want error")
	}
	// a started and must be rolled back; b attempted to start and failed
	// (so it is never stopped); c never got touched.
	for _, op := range seq {
		if op == "init:c" || op == "start:c" || op == "stop:b" || op == "stop:c" {
			t.Fatalf("unexpected operation %q in %v", op, seq)
		}
	}
	if !contains(seq, "start:b") {
		t.Fatalf("b never attempted to start: %v", seq)
	}
	if !contains(seq, "stop:a") {
		t.Fatalf("rollback did not stop a: %v", seq)
	}
	state, err := m.State("b")
	if err != nil || state != StateFailed {
		t.Fatalf("b state = %v (err %v), want failed", state, err)
	}
}

func TestUnknownDependencyFails(t *testing.T) {
	var seq []string
	m := newTestManager(t)
	mustRegister(t, m, &fakeService{name: "a", deps: []string{"ghost"}, events: &seq})
	if err := m.StartAll(context.Background()); err == nil {
		t.Fatal("StartAll with unknown dependency succeeded, want error")
	}
}

func TestDependencyCycleFails(t *testing.T) {
	var seq []string
	m := newTestManager(t)
	mustRegister(t, m,
		&fakeService{name: "a", deps: []string{"b"}, events: &seq},
		&fakeService{name: "b", deps: []string{"a"}, events: &seq},
	)
	if err := m.StartAll(context.Background()); err == nil {
		t.Fatal("StartAll with a dependency cycle succeeded, want error")
	}
}

func TestDuplicateRegisterRejected(t *testing.T) {
	var seq []string
	m := newTestManager(t)
	s := &fakeService{name: "a", events: &seq}
	if err := m.Register(s); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(s); err == nil {
		t.Fatal("duplicate registration succeeded, want error")
	}
}

func TestStopAllCollectsErrors(t *testing.T) {
	var seq []string
	m := newTestManager(t)
	mustRegister(t, m,
		&fakeService{name: "a", events: &seq},
		&fakeService{name: "b", events: &seq, stopErr: errors.New("cannot stop")},
	)
	mustStart(t, m)
	err := m.StopAll(context.Background())
	if err == nil {
		t.Fatal("StopAll succeeded, want joined error")
	}
	if state, _ := m.State("b"); state != StateFailed {
		t.Fatalf("b state = %v, want failed", state)
	}
	if state, _ := m.State("a"); state != StateStopped {
		t.Fatalf("a state = %v, want stopped (one bad service must not block others)", state)
	}
}

func TestStopAllWaitsForSlowService(t *testing.T) {
	var seq []string
	m := newTestManager(t)
	hanging := &hangingService{fakeService: fakeService{name: "hang", events: &seq}, release: make(chan struct{})}
	mustRegister(t, m, hanging)
	mustStart(t, m)

	done := make(chan error, 1)
	go func() { done <- m.StopAll(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("StopAll returned (%v) despite a service still stopping", err)
	case <-time.After(300 * time.Millisecond):
		// Expected: StopAll is waiting on the hanging service.
	}
	close(hanging.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StopAll returned error after clean stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopAll did not return after the service released")
	}
}

// hangingService blocks in Stop until release is closed.
type hangingService struct {
	fakeService
	release chan struct{}
}

func (h *hangingService) Stop(ctx context.Context) error {
	<-h.release
	return nil
}

func mustRegister(t *testing.T, m *Manager, services ...Service) {
	t.Helper()
	for _, s := range services {
		if err := m.Register(s); err != nil {
			t.Fatal(err)
		}
	}
}

func mustStart(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
