package main

import (
	"strings"
	"testing"
)

func TestAttachNonExistentRuntimeReturnsError(t *testing.T) {
	tempDir := t.TempDir()
	err := cmdAttach(tempDir)
	if err == nil {
		t.Fatal("expected error attaching to non-existent runtime, got nil")
	}
	if !strings.Contains(err.Error(), "no running Gostalgia instance found") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestUsageTextMentionsAttach(t *testing.T) {
	if !strings.Contains(usageText, "gostalgia attach") {
		t.Fatalf("expected usageText to document 'gostalgia attach'")
	}
	if !strings.Contains(usageText, "--attach") {
		t.Fatalf("expected usageText to document '--attach'")
	}
}
