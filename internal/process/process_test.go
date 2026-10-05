package process

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"gostalgia/internal/events"
)

// TestMain doubles as the child helper. When the test binary is invoked
// with GOSTALGIA_TEST_CHILD=1 it blocks forever (exercising kill-on-stop);
// with =exit it exits with code 3 (exercising failure status tracking).
// The sleep loop blocks without tripping the runtime deadlock detector,
// which fires on a bare select{} when no other goroutine exists.
func TestMain(m *testing.M) {
	switch os.Getenv("GOSTALGIA_TEST_CHILD") {
	case "1":
		for {
			time.Sleep(time.Hour)
		}
	case "exit":
		os.Exit(3)
	}
	os.Exit(m.Run())
}

type recorder struct {
	mu        sync.Mutex
	envelopes []events.Envelope
}

func (r *recorder) add(e events.Envelope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.envelopes = append(r.envelopes, e)
}

func (r *recorder) hasState(pid int32, state State) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.envelopes {
		if ev, ok := e.Payload.(Event); ok && ev.ID == pid && ev.State == state {
			return true
		}
	}
	return false
}

func newTestManager(t *testing.T) (*Manager, *recorder) {
	t.Helper()
	bus := events.NewBus()
	rec := &recorder{}
	bus.Subscribe("*", func(e events.Envelope) { rec.add(e) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewManager(bus, log), rec
}

func TestInProcLifecycle(t *testing.T) {
	m, rec := newTestManager(t)
	started := make(chan struct{})
	p, err := m.StartInProc(context.Background(), Spec{Name: "testapp"}, func(p *Process) error {
		close(started)
		<-p.Context().Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	info := p.Info()
	if info.Kind != KindInProc {
		t.Errorf("kind = %q, want inproc", info.Kind)
	}
	if info.State != StateRunning {
		t.Errorf("state = %q, want running", info.State)
	}

	if err := m.Stop(p.ID(), 2*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	info = p.Info()
	if info.State != StateStopped {
		t.Errorf("state after stop = %q, want stopped", info.State)
	}
	if !rec.hasState(p.ID(), StateStopped) {
		t.Error("no stopped event was published")
	}
	if !rec.hasState(p.ID(), StateRunning) {
		t.Error("no running event was published")
	}
}

func TestInProcRunErrorFailsProcess(t *testing.T) {
	m, _ := newTestManager(t)
	p, err := m.StartInProc(context.Background(), Spec{Name: "crasher"}, func(p *Process) error {
		return context.DeadlineExceeded // non-cancel error, no stop requested
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit")
	}
	if info := p.Info(); info.State != StateFailed {
		t.Errorf("state = %q, want failed", info.State)
	}
}

func TestStopUnknownProcess(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Stop(999, time.Second); err == nil {
		t.Fatal("stopping unknown pid succeeded, want error")
	}
}

func TestStopHonorsTimeout(t *testing.T) {
	m, _ := newTestManager(t)
	p, err := m.StartInProc(context.Background(), Spec{Name: "stubborn"}, func(p *Process) error {
		<-time.After(30 * time.Second) // ignores its context on purpose
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = m.Stop(p.ID(), 100*time.Millisecond)
	if err == nil {
		t.Fatal("stop succeeded despite a process ignoring its context")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stop took %s, timeout not honored", elapsed)
	}
}

// childHelperArgs builds an argv for invoking this test binary as a child.
// The helper mode is selected through the environment.
func childHelperArgs(t *testing.T) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe}
}

func childHelperEnv(mode string) []string {
	return append(os.Environ(), "GOSTALGIA_TEST_CHILD="+mode)
}

func TestChildKilledOnStop(t *testing.T) {
	if testing.Short() {
		t.Skip("child process test skipped in short mode")
	}
	m, rec := newTestManager(t)
	p, err := m.StartChild(context.Background(), Spec{
		Name: "sleeper",
		Args: childHelperArgs(t),
		Env:  childHelperEnv("1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the child settle into select{}

	if err := m.Stop(p.ID(), 2*time.Second); err != nil {
		t.Fatalf("stop child: %v", err)
	}
	info := p.Info()
	t.Logf("child info: state=%q exit_code=%d err=%q", info.State, info.ExitCode, info.Err)
	if info.Kind != KindChild {
		t.Errorf("kind = %q, want child", info.Kind)
	}
	if info.State != StateStopped {
		t.Errorf("state = %q, want stopped (killed by stop)", info.State)
	}
	if !rec.hasState(p.ID(), StateStopped) {
		t.Error("no stopped event for the child")
	}
}

func TestChildExitStatusTracked(t *testing.T) {
	if testing.Short() {
		t.Skip("child process test skipped in short mode")
	}
	m, _ := newTestManager(t)
	p, err := m.StartChild(context.Background(), Spec{
		Name: "quitter",
		Args: childHelperArgs(t),
		Env:  childHelperEnv("exit"),
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("child did not exit")
	}
	info := p.Info()
	if info.State != StateFailed {
		t.Errorf("state = %q, want failed", info.State)
	}
	if info.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", info.ExitCode)
	}
}

func TestChildRequiresArgs(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.StartChild(context.Background(), Spec{Name: "empty"}); err == nil {
		t.Fatal("StartChild without args succeeded, want error")
	}
}
