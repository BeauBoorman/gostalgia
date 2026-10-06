package services

import (
	"context"
	"encoding/json"
	"testing"

	"gostalgia/internal/recovery"
	"gostalgia/internal/security"
)

func TestRecoveryService_IPCWorkflow(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	caps := security.AdminCapabilities()

	// 1. Create a test user document
	docPath := "/users/guest/documents/test_doc.txt"
	must(t, env.ctx.VFS.WriteFile(docPath, []byte("Content for recovery test"), 0o644))

	// 2. Export backup via IPC
	backupPath := "/users/guest/downloads/export_test.gbar"
	exportResp := env.call(ctx, caps, "backup/export", BackupExportParams{
		Path:        backupPath,
		Description: "IPC integration test backup",
	})
	if !exportResp.OK {
		t.Fatalf("backup/export failed: %s", exportResp.Error)
	}
	var expRes recovery.ExportResult
	if err := json.Unmarshal(exportResp.Data, &expRes); err != nil {
		t.Fatal(err)
	}
	if expRes.TotalFiles == 0 {
		t.Fatalf("expected exported files > 0, got %d", expRes.TotalFiles)
	}

	// 3. Inspect backup via IPC
	inspectResp := env.call(ctx, caps, "backup/inspect", BackupPathParams{
		Path: backupPath,
	})
	if !inspectResp.OK {
		t.Fatalf("backup/inspect failed: %s", inspectResp.Error)
	}
	var manifest recovery.Manifest
	if err := json.Unmarshal(inspectResp.Data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.FormatVersion != recovery.FormatVersion {
		t.Errorf("manifest format version = %d, want %d", manifest.FormatVersion, recovery.FormatVersion)
	}

	// 4. Preview backup via IPC with identical files
	prevResp := env.call(ctx, caps, "backup/preview", BackupPathParams{
		Path: backupPath,
	})
	if !prevResp.OK {
		t.Fatalf("backup/preview failed: %s", prevResp.Error)
	}
	var preview recovery.PreviewReport
	if err := json.Unmarshal(prevResp.Data, &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Conflicts) != 0 {
		t.Errorf("expected 0 conflicts before edits, got %d", len(preview.Conflicts))
	}

	// 5. Mutate the file to produce a conflict
	must(t, env.ctx.VFS.WriteFile(docPath, []byte("Locally edited content"), 0o644))

	prevResp2 := env.call(ctx, caps, "backup/preview", BackupPathParams{
		Path: backupPath,
	})
	if !prevResp2.OK {
		t.Fatalf("backup/preview failed: %s", prevResp2.Error)
	}
	var preview2 recovery.PreviewReport
	if err := json.Unmarshal(prevResp2.Data, &preview2); err != nil {
		t.Fatal(err)
	}
	if len(preview2.Conflicts) != 1 {
		t.Errorf("expected 1 conflict, got %d", len(preview2.Conflicts))
	}

	// 6. Restore with ConflictAbort -> should fail
	restoreAbort := env.call(ctx, caps, "backup/restore", BackupRestoreParams{
		Path:     backupPath,
		Strategy: recovery.ConflictAbort,
	})
	if restoreAbort.OK {
		t.Fatal("expected restore with abort to fail on conflict")
	}

	// 7. Restore with ConflictOverwrite -> should succeed and overwrite
	restoreOverwrite := env.call(ctx, caps, "backup/restore", BackupRestoreParams{
		Path:     backupPath,
		Strategy: recovery.ConflictOverwrite,
	})
	if !restoreOverwrite.OK {
		t.Fatalf("restore with overwrite failed: %s", restoreOverwrite.Error)
	}
	var rep recovery.RestoreReport
	if err := json.Unmarshal(restoreOverwrite.Data, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.RestoredCount == 0 {
		t.Errorf("expected restored count > 0, got %d", rep.RestoredCount)
	}

	// Verify content was restored
	restoredData, err := env.ctx.VFS.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredData) != "Content for recovery test" {
		t.Fatalf("content was not restored: %s", string(restoredData))
	}
}

func TestRecoveryService_CapabilityEnforcement(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// No capabilities
	noCaps := security.NewCapabilities()

	// Try export without backup.write
	resp := env.call(ctx, noCaps, "backup/export", BackupExportParams{
		Path: "/users/guest/downloads/test.gbar",
	})
	if resp.OK {
		t.Fatal("expected backup/export without capabilities to fail")
	}

	// Try inspect without backup.read
	resp = env.call(ctx, noCaps, "backup/inspect", BackupPathParams{
		Path: "/users/guest/downloads/test.gbar",
	})
	if resp.OK {
		t.Fatal("expected backup/inspect without capabilities to fail")
	}

	// Try preview without backup.read
	resp = env.call(ctx, noCaps, "backup/preview", BackupPathParams{
		Path: "/users/guest/downloads/test.gbar",
	})
	if resp.OK {
		t.Fatal("expected backup/preview without capabilities to fail")
	}

	// Try restore without backup.write
	resp = env.call(ctx, noCaps, "backup/restore", BackupRestoreParams{
		Path:     "/users/guest/downloads/test.gbar",
		Strategy: recovery.ConflictOverwrite,
	})
	if resp.OK {
		t.Fatal("expected backup/restore without capabilities to fail")
	}
}
