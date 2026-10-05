// Package session implements environment sessions: a logged-in user's
// span of activity, to which applications and processes are attached.
package session

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"gostalgia/internal/events"
	"gostalgia/internal/security"
)

// Session is one user's login session.
type Session struct {
	ID        string        `json:"id"`
	User      security.User `json:"user"`
	StartedAt time.Time     `json:"started_at"`
	StoppedAt time.Time     `json:"stopped_at,omitempty"`

	mu      sync.Mutex
	stopped bool
}

// Active reports whether the session is still open.
func (s *Session) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stopped
}

func (s *Session) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped {
		s.stopped = true
		s.StoppedAt = time.Now()
	}
}

// Event is published when a session opens or closes.
type Event struct {
	ID    string `json:"id"`
	User  string `json:"user"`
	State string `json:"state"` // "started" or "stopped"
}

func (Event) Type() string { return "session.state" }

// Manager creates and tracks sessions.
type Manager struct {
	mu       sync.Mutex
	next     int
	sessions map[string]*Session
	bus      *events.Bus
	log      *slog.Logger
}

func NewManager(bus *events.Bus, log *slog.Logger) *Manager {
	return &Manager{
		sessions: map[string]*Session{},
		bus:      bus,
		log:      log,
	}
}

// Create opens a session for user.
func (m *Manager) Create(user security.User) (*Session, error) {
	if user.ID == "" {
		return nil, fmt.Errorf("session: user id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	s := &Session{
		ID:        fmt.Sprintf("session-%d", m.next),
		User:      user,
		StartedAt: time.Now(),
	}
	m.sessions[s.ID] = s
	m.bus.Publish("session", Event{ID: s.ID, User: user.Name, State: "started"})
	m.log.Info("session started", "session", s.ID, "user", user.Name)
	return s, nil
}

// Close closes a session by ID.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("session: no such session %q", id)
	}
	s.close()
	m.bus.Publish("session", Event{ID: s.ID, User: s.User.Name, State: "stopped"})
	m.log.Info("session stopped", "session", s.ID, "user", s.User.Name)
	return nil
}

// Get returns a session by ID.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Active lists open sessions, sorted by ID.
func (m *Manager) Active() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.Active() {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
