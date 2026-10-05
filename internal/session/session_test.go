package session

import (
	"io"
	"log/slog"
	"sync"
	"testing"

	"gostalgia/internal/events"
	"gostalgia/internal/security"
)

type recorder struct {
	mu   sync.Mutex
	seen []Event
}

func (r *recorder) add(e events.Envelope) {
	if ev, ok := e.Payload.(Event); ok {
		r.mu.Lock()
		r.seen = append(r.seen, ev)
		r.mu.Unlock()
	}
}

func (r *recorder) count(state string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.seen {
		if ev.State == state {
			n++
		}
	}
	return n
}

func newTestManager(t *testing.T) (*Manager, *recorder) {
	t.Helper()
	bus := events.NewBus()
	rec := &recorder{}
	bus.Subscribe("*", func(e events.Envelope) { rec.add(e) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewManager(bus, log), rec
}

func TestCreateAndCloseSession(t *testing.T) {
	m, rec := newTestManager(t)
	s, err := m.Create(security.User{ID: "u-guest", Name: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Active() {
		t.Fatal("fresh session is not active")
	}
	if s.ID == "" {
		t.Fatal("session id is empty")
	}
	if err := m.Close(s.ID); err != nil {
		t.Fatal(err)
	}
	if s.Active() {
		t.Fatal("closed session is still active")
	}
	if rec.count("started") != 1 || rec.count("stopped") != 1 {
		t.Fatalf("events: started=%d stopped=%d, want 1 and 1", rec.count("started"), rec.count("stopped"))
	}
	if len(m.Active()) != 0 {
		t.Fatal("closed session still listed as active")
	}
}

func TestCloseUnknownSession(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Close("nope"); err == nil {
		t.Fatal("closing unknown session succeeded, want error")
	}
}

func TestCreateRequiresUserID(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.Create(security.User{}); err == nil {
		t.Fatal("creating session for empty user succeeded, want error")
	}
}

func TestSessionsGetDistinctIDs(t *testing.T) {
	m, _ := newTestManager(t)
	a, _ := m.Create(security.User{ID: "u-1", Name: "one"})
	b, _ := m.Create(security.User{ID: "u-2", Name: "two"})
	if a.ID == b.ID {
		t.Fatalf("two sessions share id %q", a.ID)
	}
	if len(m.Active()) != 2 {
		t.Fatalf("active = %d, want 2", len(m.Active()))
	}
}
