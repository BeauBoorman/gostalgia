package process

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func startIdleProcess(t *testing.T, m *Manager, name string) *Process {
	t.Helper()
	p, err := m.StartInProc(context.Background(), Spec{Name: name}, func(p *Process) error {
		<-p.Context().Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestStopAlreadyStoppedProcess: Stop on a process that already exited on
// its own returns cleanly instead of erroring or hanging.
func TestStopAlreadyStoppedProcess(t *testing.T) {
	m, _ := newTestManager(t)
	p, err := m.StartInProc(context.Background(), Spec{Name: "shortlived"}, func(p *Process) error {
		return nil // exit immediately
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("self-exiting process never finished")
	}
	if err := m.Stop(p.ID(), time.Second); err != nil {
		t.Fatalf("Stop on an already-stopped process: %v", err)
	}
}

// TestConcurrentStopSameProcess: concurrent Stops on one pid must all
// return promptly and leave the process stopped exactly once (run under
// -race).
func TestConcurrentStopSameProcess(t *testing.T) {
	m, _ := newTestManager(t)
	p := startIdleProcess(t, m, "contended")

	const racers = 8
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = m.Stop(p.ID(), 5*time.Second)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d: Stop: %v", i, err)
		}
	}
	if info := p.Info(); info.State != StateStopped {
		t.Errorf("state after concurrent stops = %q, want stopped", info.State)
	}
}

// TestStopAllOverMixedStates: StopAll stops the live processes, tolerates
// the already-stopped and failed ones, and terminates without hanging.
func TestStopAllOverMixedStates(t *testing.T) {
	m, _ := newTestManager(t)
	live := startIdleProcess(t, m, "live")

	selfExited, err := m.StartInProc(context.Background(), Spec{Name: "self-exited"}, func(p *Process) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-selfExited.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("self-exiting process never finished")
	}

	failed, err := m.StartInProc(context.Background(), Spec{Name: "failed"}, func(p *Process) error {
		return errors.New("intentional failure")
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-failed.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("failing process never finished")
	}

	done := make(chan struct{})
	go func() { m.StopAll(5 * time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StopAll hung over mixed process states")
	}

	if info := live.Info(); info.State != StateStopped {
		t.Errorf("live process state = %q, want stopped", info.State)
	}
	if m.Count() != 0 {
		t.Errorf("live process count = %d, want 0", m.Count())
	}
	// Exited processes remain listed (reaping is backlog #3).
	if got := len(m.List()); got != 3 {
		t.Errorf("listed processes = %d, want 3 (no reaping yet)", got)
	}
}
