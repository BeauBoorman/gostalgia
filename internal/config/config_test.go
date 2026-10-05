package config

import (
	"path/filepath"
	"testing"
)

func TestLoadMissingFileGivesEmptyStore(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Value("anything"); ok {
		t.Fatal("empty store returned a value")
	}
}

func TestSetPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("log.level", "debug"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("ui.theme.colors.primary", "#3366ff"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.String("log.level", ""); got != "debug" {
		t.Errorf("log.level = %q, want debug", got)
	}
	if got := reloaded.String("ui.theme.colors.primary", ""); got != "#3366ff" {
		t.Errorf("nested value = %q, want #3366ff", got)
	}
}

func TestSetConflictWithPathThroughScalar(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "system.json"))
	if err := s.Set("a.b", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("a.b.c", 2); err == nil {
		t.Fatal("setting through a scalar path succeeded, want error")
	}
}

func TestTypedAccessors(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "system.json"))
	if err := s.Set("port", 8080); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("verbose", true); err != nil {
		t.Fatal(err)
	}
	if got := s.Int("port", 0); got != 8080 {
		t.Errorf("port = %d, want 8080", got)
	}
	if !s.Bool("verbose", false) {
		t.Error("verbose = false, want true")
	}
	if got := s.String("missing", "fallback"); got != "fallback" {
		t.Errorf("missing string = %q, want fallback", got)
	}
	if got := s.Int("missing", 42); got != 42 {
		t.Errorf("missing int = %d, want 42", got)
	}
}

func TestSnapshotIsDeepCopy(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "system.json"))
	if err := s.Set("x.y", 1); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	snap["x"].(map[string]any)["y"] = 2
	if v, _ := s.Value("x.y"); v != 1 {
		t.Fatalf("snapshot mutation leaked into the store: x.y = %v", v)
	}
}
