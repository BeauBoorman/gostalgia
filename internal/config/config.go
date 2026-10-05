// Package config implements the environment's configuration store: one
// JSON document per environment, addressed by dotted paths, persisted
// atomically on write. Defaults live in code (see runtime.Boot); per-user
// and per-app layers will overlay this store in later milestones.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store is the configuration document for one environment.
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]any
}

// Load opens the store at path. A missing file yields an empty store
// (defaults are applied by callers); a corrupt file is an error.
func Load(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]any{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return s, nil
}

// Value returns the value at a dotted path ("log.level") and whether it
// was set.
func (s *Store) Value(path string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return lookup(s.data, strings.Split(path, "."))
}

// String returns the string at path, or def.
func (s *Store) String(path, def string) string {
	v, ok := s.Value(path)
	if !ok {
		return def
	}
	if str, ok := v.(string); ok {
		return str
	}
	return fmt.Sprintf("%v", v)
}

// Int returns the integer at path, or def.
func (s *Store) Int(path string, def int) int {
	v, ok := s.Value(path)
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64: // JSON numbers
		return int(n)
	case int:
		return n
	default:
		return def
	}
}

// Bool returns the boolean at path, or def.
func (s *Store) Bool(path string, def bool) bool {
	v, ok := s.Value(path)
	if !ok {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

// Set stores v at the dotted path, creating intermediate objects, and
// persists the document. It is an error to set a path through an
// existing non-object value.
func (s *Store) Set(path string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	segs := strings.Split(path, ".")
	if len(segs) == 0 || segs[0] == "" {
		return errors.New("config: empty path")
	}
	m := s.data
	for i, seg := range segs[:len(segs)-1] {
		next, ok := m[seg]
		if !ok {
			next = map[string]any{}
			m[seg] = next
		}
		nm, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("config: %q conflicts with a non-object value", strings.Join(segs[:i+1], "."))
		}
		m = nm
	}
	m[segs[len(segs)-1]] = v
	return s.saveLocked()
}

// Snapshot returns a deep copy of the document.
func (s *Store) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(s.data)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

// Path returns the backing file path.
func (s *Store) Path() string { return s.path }

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	b = append(b, '\n')
	tmp := s.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("config: write: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}

func lookup(m map[string]any, segs []string) (any, bool) {
	var cur any = m
	for _, seg := range segs {
		cm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = cm[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}
