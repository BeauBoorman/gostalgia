package e2e

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gostalgia/internal/recovery"
	"gostalgia/internal/runtime"
)

func TestRecoveryEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Environment 1: Source
	root1 := t.TempDir()
	rt1, err := runtime.Boot(ctx, runtime.Options{
		Root:      root1,
		LogOutput: io.Discard,
	})
	if err != nil {
		t.Fatalf("boot env 1: %v", err)
	}
	defer rt1.Shutdown("test")

	client1 := dialRunning(t, root1)
	defer client1.Close()

	// Create a profile 'author' in env 1
	var createdProf struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	must(t, client1.Call(ctx, "profile/create", map[string]string{
		"id":   "author",
		"name": "Book Author",
	}, &createdProf))

	// Write document in author's workspace
	docContent := "Chapter 1: The Portable System"
	must(t, client1.Call(ctx, "fs/write", map[string]string{
		"path":        "/users/author/documents/chapter1.txt",
		"data_base64": base64.StdEncoding.EncodeToString([]byte(docContent)),
	}, nil))

	// Export backup via IPC
	backupVFSPath := "/users/author/downloads/backup.gbar"
	var expResult recovery.ExportResult
	must(t, client1.Call(ctx, "backup/export", recovery.ExportParams{
		Path:        backupVFSPath,
		Description: "E2E portable backup",
	}, &expResult))

	if expResult.TotalFiles < 2 {
		t.Fatalf("expected at least 2 files in backup, got %d", expResult.TotalFiles)
	}

	// Read backup file from host disk of env 1
	archiveHostPath := filepath.Join(root1, "vfs", "users", "author", "downloads", "backup.gbar")
	archiveBytes, err := os.ReadFile(archiveHostPath)
	if err != nil {
		t.Fatalf("read archive from host disk: %v", err)
	}

	// Environment 2: Clean destination
	root2 := t.TempDir()
	rt2, err := runtime.Boot(ctx, runtime.Options{
		Root:      root2,
		LogOutput: io.Discard,
	})
	if err != nil {
		t.Fatalf("boot env 2: %v", err)
	}
	defer rt2.Shutdown("test")

	client2 := dialRunning(t, root2)
	defer client2.Close()

	// Stage backup into env 2's VFS
	destBackupVFSPath := "/users/guest/downloads/imported.gbar"
	must(t, client2.Call(ctx, "fs/write", map[string]string{
		"path":        destBackupVFSPath,
		"data_base64": base64.StdEncoding.EncodeToString(archiveBytes),
	}, nil))

	// Inspect via env 2 IPC
	var manifest recovery.Manifest
	must(t, client2.Call(ctx, "backup/inspect", recovery.PathParams{
		Path: destBackupVFSPath,
	}, &manifest))
	if manifest.Description != "E2E portable backup" {
		t.Errorf("manifest description = %q, want 'E2E portable backup'", manifest.Description)
	}

	// Preview via env 2 IPC
	var preview recovery.PreviewReport
	must(t, client2.Call(ctx, "backup/preview", recovery.PathParams{
		Path: destBackupVFSPath,
	}, &preview))
	if len(preview.Create) == 0 {
		t.Error("expected files to create in preview")
	}

	// Restore via env 2 IPC
	var rep recovery.RestoreReport
	must(t, client2.Call(ctx, "backup/restore", recovery.RestoreParams{
		Path:     destBackupVFSPath,
		Strategy: recovery.ConflictOverwrite,
	}, &rep))
	if rep.RestoredCount == 0 {
		t.Errorf("expected restored count > 0, got %d", rep.RestoredCount)
	}

	// Verify author document is readable in env 2
	var docRead struct {
		Data string `json:"data_base64"`
	}
	must(t, client2.Call(ctx, "fs/read", map[string]string{
		"path": "/users/author/documents/chapter1.txt",
	}, &docRead))
	data, err := base64.StdEncoding.DecodeString(docRead.Data)
	must(t, err)
	if string(data) != docContent {
		t.Fatalf("restored document mismatch: %s", string(data))
	}
}
