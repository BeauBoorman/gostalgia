package main

import (
	"context"
	"io"
	"testing"

	"gostalgia/internal/runtime"
)

func TestBackupCLIOverIPC(t *testing.T) {
	ctx := context.Background()
	rt, err := runtime.Boot(ctx, runtime.Options{Root: t.TempDir(), LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Shutdown("test")

	client, err := connect(rt.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	backupPath := "/users/guest/documents/test_backup.gbar"

	// 1. Export backup via CLI
	for _, alias := range []string{"backup", "recovery"} {
		if err := run(ctx, client, alias, []string{"export", backupPath, "--description", "cli test"}); err != nil {
			t.Fatalf("%s export failed: %v", alias, err)
		}
	}

	// 2. Inspect backup via CLI
	if err := run(ctx, client, "backup", []string{"inspect", backupPath}); err != nil {
		t.Fatalf("backup inspect failed: %v", err)
	}

	// 3. Preview backup via CLI
	if err := run(ctx, client, "backup", []string{"preview", backupPath}); err != nil {
		t.Fatalf("backup preview failed: %v", err)
	}

	// 4. Restore backup via CLI with overwrite
	if err := run(ctx, client, "backup", []string{"restore", backupPath, "--strategy", "overwrite"}); err != nil {
		t.Fatalf("backup restore failed: %v", err)
	}

	// 5. Invalid command error checking
	if err := run(ctx, client, "backup", []string{"restore"}); err == nil {
		t.Fatal("expected restore without path to fail")
	}
}
