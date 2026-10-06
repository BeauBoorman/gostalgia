package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayerPrecedence(t *testing.T) {
	dir := t.TempDir()
	sysPath := filepath.Join(dir, "system.json")
	userPath := filepath.Join(dir, "user.json")

	ls, err := NewLayeredStore(sysPath, userPath)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Defaults layer
	if val := ls.String("theme", "", QueryOpts{}); val != "nostalgia" {
		t.Fatalf("expected default theme nostalgia, got %q", val)
	}
	exp := ls.Explain("theme", QueryOpts{})
	if exp.Winner != LayerDefault {
		t.Errorf("expected winner default, got %s", exp.Winner)
	}

	// 2. System layer override
	if err := ls.Set(LayerSystem, "theme", "midnight", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if val := ls.String("theme", "", QueryOpts{}); val != "midnight" {
		t.Fatalf("expected system theme midnight, got %q", val)
	}
	exp = ls.Explain("theme", QueryOpts{})
	if exp.Winner != LayerSystem {
		t.Errorf("expected winner system, got %s", exp.Winner)
	}

	// 3. User layer override
	if err := ls.Set(LayerUser, "theme", "monochrome", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if val := ls.String("theme", "", QueryOpts{}); val != "monochrome" {
		t.Fatalf("expected user theme monochrome, got %q", val)
	}
	exp = ls.Explain("theme", QueryOpts{})
	if exp.Winner != LayerUser {
		t.Errorf("expected winner user, got %s", exp.Winner)
	}

	// 4. App layer override (with AppID)
	appOpts := QueryOpts{AppID: "com.gostalgia.notes"}
	if err := ls.Set(LayerApp, "theme", "high-contrast", appOpts); err != nil {
		t.Fatal(err)
	}
	// App query sees app theme
	if val := ls.String("theme", "", appOpts); val != "high-contrast" {
		t.Fatalf("expected app theme high-contrast, got %q", val)
	}
	// General query still sees user theme
	if val := ls.String("theme", "", QueryOpts{}); val != "monochrome" {
		t.Fatalf("expected general theme monochrome, got %q", val)
	}

	// 5. Preview layer override (highest precedence)
	if err := ls.Preview("theme", "high-contrast-light", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if val := ls.String("theme", "", QueryOpts{}); val != "high-contrast-light" {
		t.Fatalf("expected preview theme high-contrast-light, got %q", val)
	}
	if val := ls.String("theme", "", appOpts); val != "high-contrast-light" {
		t.Fatalf("expected preview theme to override app as well, got %q", val)
	}
	exp = ls.Explain("theme", appOpts)
	if exp.Winner != LayerPreview {
		t.Errorf("expected winner preview, got %s", exp.Winner)
	}
}

func TestPreviewDoesNotAccidentallyCommit(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.json")

	ls, err := NewLayeredStore("", userPath)
	if err != nil {
		t.Fatal(err)
	}

	// Set saved user theme
	if err := ls.Set(LayerUser, "theme", "midnight", QueryOpts{}); err != nil {
		t.Fatal(err)
	}

	// Preview another theme
	if err := ls.Preview("theme", "monochrome", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if !ls.HasActivePreview() {
		t.Fatal("expected active preview")
	}
	if got := ls.String("theme", "", QueryOpts{}); got != "monochrome" {
		t.Errorf("previewed theme = %q, want monochrome", got)
	}

	// Read file from disk directly to verify it was NOT modified
	reloaded, err := Load(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if diskTheme := reloaded.String("theme", ""); diskTheme != "midnight" {
		t.Fatalf("disk theme was modified by preview! got %q, want midnight", diskTheme)
	}

	// Cancel preview
	ls.CancelPreview(QueryOpts{})
	if ls.HasActivePreview() {
		t.Fatal("expected preview to be canceled")
	}
	if got := ls.String("theme", "", QueryOpts{}); got != "midnight" {
		t.Errorf("after cancel, theme = %q, want midnight", got)
	}

	// Now preview and explicitly commit
	if err := ls.Preview("theme", "high-contrast", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := ls.CommitPreview(LayerUser, QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if ls.HasActivePreview() {
		t.Fatal("expected preview to be cleared after commit")
	}

	// Verify disk file updated after commit
	reloaded, err = Load(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if diskTheme := reloaded.String("theme", ""); diskTheme != "high-contrast" {
		t.Fatalf("disk theme after commit = %q, want high-contrast", diskTheme)
	}
}

func TestCorruptSettingsFileRecovery(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "corrupt_user.json")

	// Write invalid JSON
	corruptContent := []byte(`{ "theme": "midnight", "broken": `)
	if err := os.WriteFile(userPath, corruptContent, 0o600); err != nil {
		t.Fatal(err)
	}

	// NewLayeredStore should not crash
	ls, err := NewLayeredStore("", userPath)
	if err != nil {
		t.Fatalf("expected graceful handle of corrupt file, got error: %v", err)
	}

	// Check CorruptLayers
	corrupts := ls.CorruptLayers()
	if _, ok := corrupts[LayerUser]; !ok {
		t.Fatal("expected LayerUser to be marked corrupt")
	}

	// Store should fall back to default
	if theme := ls.String("theme", "", QueryOpts{}); theme != "nostalgia" {
		t.Errorf("expected fallback to default theme nostalgia, got %q", theme)
	}

	// Corrupt file on disk must still exist and NOT be overwritten yet
	b, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(corruptContent) {
		t.Fatal("corrupt file content was modified prematurely")
	}

	// Writing a valid setting should recover and clear corrupt status
	if err := ls.Set(LayerUser, "theme", "monochrome", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if len(ls.CorruptLayers()) != 0 {
		t.Errorf("expected corrupt status to clear after valid set, got %v", ls.CorruptLayers())
	}

	// Verify file is now valid JSON on disk
	reloaded, err := Load(userPath)
	if err != nil {
		t.Fatalf("reloading recovered file failed: %v", err)
	}
	if reloaded.String("theme", "") != "monochrome" {
		t.Errorf("recovered theme = %q, want monochrome", reloaded.String("theme", ""))
	}
}

func TestKeybindingConflictRejection(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.json")

	ls, err := NewLayeredStore("", userPath)
	if err != nil {
		t.Fatal(err)
	}

	// Default has shortcuts.home = f1
	if got := ls.String("shortcuts.home", "", QueryOpts{}); got != "f1" {
		t.Fatalf("expected shortcuts.home = f1, got %q", got)
	}

	// Attempting to set shortcuts.settings = f1 should be rejected as a conflict
	err = ls.Set(LayerUser, "shortcuts.settings", "f1", QueryOpts{})
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}

	// Verify setting was NOT stored
	if val := ls.String("shortcuts.settings", "", QueryOpts{}); val != "f7" {
		t.Errorf("expected shortcuts.settings to remain f7, got %q", val)
	}
}

func TestResetLayerAndPath(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.json")

	ls, err := NewLayeredStore("", userPath)
	if err != nil {
		t.Fatal(err)
	}

	// Set user settings
	if err := ls.Set(LayerUser, "theme", "midnight", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := ls.Set(LayerUser, "accessibility.reduced_motion", true, QueryOpts{}); err != nil {
		t.Fatal(err)
	}

	// Reset single path
	if err := ls.Reset(LayerUser, "theme", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if theme := ls.String("theme", "", QueryOpts{}); theme != "nostalgia" {
		t.Errorf("expected theme to reset to nostalgia, got %q", theme)
	}
	if !ls.Bool("accessibility.reduced_motion", false, QueryOpts{}) {
		t.Error("expected reduced_motion to remain true")
	}

	// Reset entire layer
	if err := ls.Reset(LayerUser, "", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if ls.Bool("accessibility.reduced_motion", false, QueryOpts{}) {
		t.Error("expected reduced_motion to reset to default false")
	}
}

func TestChangeEvents(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.json")

	ls, err := NewLayeredStore("", userPath)
	if err != nil {
		t.Fatal(err)
	}

	var events []ChangeEvent
	ls.OnChange(func(ev ChangeEvent) {
		events = append(events, ev)
	})

	// 1. Set
	if err := ls.Set(LayerUser, "theme", "midnight", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Layer != LayerUser || events[0].Value != "midnight" {
		t.Fatalf("unexpected event: %+v", events)
	}

	// 2. Preview
	if err := ls.Preview("theme", "monochrome", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || !events[1].IsPreview || events[1].Value != "monochrome" {
		t.Fatalf("unexpected preview event: %+v", events[1])
	}

	// 3. Cancel preview
	ls.CancelPreview(QueryOpts{})
	if len(events) != 3 || events[2].IsPreview || events[2].Value != "midnight" {
		t.Fatalf("unexpected cancel event: %+v", events[2])
	}
}

func TestSetUserStore(t *testing.T) {
	dir := t.TempDir()
	sysPath := filepath.Join(dir, "system.json")
	user1Path := filepath.Join(dir, "user1.json")
	user2Path := filepath.Join(dir, "user2.json")

	ls, err := NewLayeredStore(sysPath, user1Path)
	if err != nil {
		t.Fatal(err)
	}

	// Set preference for user 1
	if err := ls.Set(LayerUser, "theme", "midnight", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if theme := ls.String("theme", "", QueryOpts{}); theme != "midnight" {
		t.Fatalf("user 1 theme = %q, want midnight", theme)
	}

	// Switch to user 2
	if err := ls.SetUserStore(user2Path); err != nil {
		t.Fatal(err)
	}
	// user 2 has no preference, falls back to default nostalgia
	if theme := ls.String("theme", "", QueryOpts{}); theme != "nostalgia" {
		t.Fatalf("user 2 theme = %q, want default nostalgia", theme)
	}

	// Set preference for user 2
	if err := ls.Set(LayerUser, "theme", "monochrome", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	if theme := ls.String("theme", "", QueryOpts{}); theme != "monochrome" {
		t.Fatalf("user 2 theme = %q, want monochrome", theme)
	}

	// Switch back to user 1
	if err := ls.SetUserStore(user1Path); err != nil {
		t.Fatal(err)
	}
	if theme := ls.String("theme", "", QueryOpts{}); theme != "midnight" {
		t.Fatalf("user 1 restored theme = %q, want midnight", theme)
	}
}
