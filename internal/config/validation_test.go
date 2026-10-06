package config

import (
	"strings"
	"testing"
)

func TestValidationRules(t *testing.T) {
	// Valid themes
	for _, theme := range []string{"nostalgia", "midnight", "monochrome", "high-contrast", "high-contrast-light"} {
		if err := Validate("theme", theme); err != nil {
			t.Errorf("expected theme %q to be valid: %v", theme, err)
		}
	}
	// Invalid themes
	for _, badTheme := range []string{"neon", "rainbow", "", "123"} {
		if err := Validate("theme", badTheme); err == nil {
			t.Errorf("expected bad theme %q to fail validation", badTheme)
		}
	}

	// Valid color modes
	for _, mode := range []string{"plain", "ansi16", "ansi256", "truecolor"} {
		if err := Validate("accessibility.color_mode", mode); err != nil {
			t.Errorf("expected color mode %q to be valid: %v", mode, err)
		}
	}
	// Invalid color mode
	if err := Validate("accessibility.color_mode", "rgb"); err == nil {
		t.Error("expected invalid color mode to fail")
	}

	// Booleans
	if err := Validate("accessibility.reduced_motion", true); err != nil {
		t.Error(err)
	}
	if err := Validate("accessibility.reduced_motion", "true"); err == nil {
		t.Error("expected string for boolean field to fail")
	}

	// Startup views
	for _, v := range []string{"home", "launcher", "prompt"} {
		if err := Validate("startup.view", v); err != nil {
			t.Errorf("expected startup view %q to be valid: %v", v, err)
		}
	}
	if err := Validate("startup.view", "desktop"); err == nil {
		t.Error("expected invalid startup view to fail")
	}

	// Startup app
	if err := Validate("startup.app", "com.gostalgia.notes"); err != nil {
		t.Error(err)
	}
	if err := Validate("startup.app", ""); err != nil {
		t.Error("empty startup app should be allowed")
	}
	if err := Validate("startup.app", "invalid_app_id"); err == nil {
		t.Error("expected invalid app id to fail")
	}

	// Toast limit
	if err := Validate("notifications.toast_limit", 5); err != nil {
		t.Error(err)
	}
	if err := Validate("notifications.toast_limit", -1); err == nil {
		t.Error("expected negative toast limit to fail")
	}
}

func TestKeybindingConflictDetection(t *testing.T) {
	shortcuts := map[string]string{
		"home":          "f1",
		"launcher":      "f2",
		"tasks":         "f5",
		"notifications": "f6",
		"settings":      "f7",
		"palette":       "ctrl+p",
	}

	conflicts := DetectKeybindingConflicts(shortcuts)
	if len(conflicts) != 0 {
		t.Fatalf("expected 0 conflicts, got %d", len(conflicts))
	}

	// Add conflict
	shortcuts["settings"] = "f1"
	conflicts = DetectKeybindingConflicts(shortcuts)
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].Key != "f1" {
		t.Errorf("conflict key = %q, want f1", conflicts[0].Key)
	}
	if len(conflicts[0].Actions) != 2 || conflicts[0].Actions[0] != "home" || conflicts[0].Actions[1] != "settings" {
		t.Errorf("unexpected actions: %v", conflicts[0].Actions)
	}

	// Test ValidateBatch returns KeybindingConflictError
	batch := map[string]any{
		"shortcuts": map[string]any{
			"home":     "f1",
			"settings": "f1",
		},
	}
	err := ValidateBatch(batch)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "keybinding conflicts") {
		t.Errorf("unexpected error message: %v", err)
	}
}
