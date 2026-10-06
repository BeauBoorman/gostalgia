package shell

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gostalgia/internal/recovery"
)

type backupCaller struct {
	method string
	params any
}

func (c *backupCaller) Call(_ context.Context, method string, params, result any) error {
	c.method = method
	c.params = params
	*result.(*json.RawMessage) = json.RawMessage(`{"status":"ok"}`)
	return nil
}

func TestBackupShellCommands(t *testing.T) {
	// Test export with relative path
	for _, line := range []string{`backup export "downloads/archive.gbar" --description "test note"`, `recovery export C:\users\guest\downloads\archive.gbar --description "test note"`} {
		c := &backupCaller{}
		text, cwd, quit, err := command(context.Background(), c, "/users/guest", line)
		if err != nil || quit || cwd != "/users/guest" || !strings.Contains(text, "status") {
			t.Fatalf("unexpected result: %s, %v", text, err)
		}
		if c.method != "backup/export" {
			t.Fatalf("method = %s, want backup/export", c.method)
		}
		p, ok := c.params.(recovery.ExportParams)
		if !ok || p.Path != "/users/guest/downloads/archive.gbar" || p.Description != "test note" {
			t.Fatalf("unexpected export params: %+v", c.params)
		}
	}

	// Test inspect
	{
		c := &backupCaller{}
		text, cwd, quit, err := command(context.Background(), c, "/users/guest", `backup inspect "downloads/archive.gbar"`)
		if err != nil || quit || cwd != "/users/guest" || !strings.Contains(text, "status") {
			t.Fatalf("unexpected result: %s, %v", text, err)
		}
		if c.method != "backup/inspect" {
			t.Fatalf("method = %s, want backup/inspect", c.method)
		}
		p, ok := c.params.(recovery.PathParams)
		if !ok || p.Path != "/users/guest/downloads/archive.gbar" {
			t.Fatalf("unexpected inspect params: %+v", c.params)
		}
	}

	// Test restore with options
	{
		c := &backupCaller{}
		text, cwd, quit, err := command(context.Background(), c, "/users/guest", `backup restore "downloads/archive.gbar" --strategy overwrite --profile guest`)
		if err != nil || quit || cwd != "/users/guest" || !strings.Contains(text, "status") {
			t.Fatalf("unexpected result: %s, %v", text, err)
		}
		if c.method != "backup/restore" {
			t.Fatalf("method = %s, want backup/restore", c.method)
		}
		p, ok := c.params.(recovery.RestoreParams)
		if !ok || p.Path != "/users/guest/downloads/archive.gbar" || p.Strategy != recovery.ConflictOverwrite || p.ProfileFilter != "guest" {
			t.Fatalf("unexpected restore params: %+v", c.params)
		}
	}

	// Invalid command
	c := &backupCaller{}
	if _, _, _, err := command(context.Background(), c, "/", "backup invalid"); err == nil || c.method != "" {
		t.Fatal("invalid backup command should fail before IPC")
	}
}

func TestBackupAutocomplete(t *testing.T) {
	m := &Model{}

	// Tab-completing "back" should complete to "backup"
	m.input = []rune("back")
	m.cursor = 4
	m.complete()
	if string(m.input) != "backup" {
		t.Errorf("complete 'back' = %q, want 'backup'", string(m.input))
	}

	// Tab-completing "backup exp" should complete to "backup export"
	m.input = []rune("backup exp")
	m.cursor = 10
	m.complete()
	if string(m.input) != "backup export" {
		t.Errorf("complete 'backup exp' = %q, want 'backup export'", string(m.input))
	}
}
