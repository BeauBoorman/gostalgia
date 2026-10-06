package recovery_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"gostalgia/internal/recovery"
	"gostalgia/internal/vfs"
)

func newTestVFS(t *testing.T) *vfs.VFS {
	t.Helper()
	v := vfs.New(vfs.NewMem())
	for _, dir := range []string{
		"/config",
		"/users/guest/documents",
		"/users/guest/downloads",
		"/users/guest/desktop",
		"/users/guest/config",
		"/users/alice/documents",
		"/users/alice/config",
		"/tmp",
	} {
		if err := v.MkdirAll(dir); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return v
}

func populateTestData(t *testing.T, v *vfs.VFS) {
	t.Helper()
	// System configuration
	if err := v.WriteFile("/config/system.json", []byte(`{"theme":"nostalgia"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Profiles registry
	profilesJSON := `{"active":"guest","profiles":[{"id":"guest","name":"Guest"},{"id":"alice","name":"Alice"}]}`
	if err := v.WriteFile("/config/profiles.json", []byte(profilesJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// Guest document and settings
	if err := v.WriteFile("/users/guest/documents/notes.txt", []byte("Hello Gostalgia"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := v.WriteFile("/users/guest/config/user.json", []byte(`{"theme":"midnight"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Guest workspace with history to be sanitized
	workspaceJSON := `{"cwd":"/users/guest/documents","history":["ls","cat notes.txt","auth token 1234567890abcdef"]}`
	if err := v.WriteFile("/users/guest/config/workspace.json", []byte(workspaceJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// Alice document
	if err := v.WriteFile("/users/alice/documents/secret.txt", []byte("Alice's safe notes"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExportAndInspectRoundTrip(t *testing.T) {
	v := newTestVFS(t)
	populateTestData(t, v)

	buf := new(bytes.Buffer)
	opts := recovery.ExportOptions{
		IncludeSystem:  true,
		SourceVersion:  "0.1.0",
		Description:    "Test export",
		ExcludeSecrets: []string{"live-operator-token-abc12345"},
	}

	res, err := recovery.Export(context.Background(), v, buf, opts)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}
	if res.TotalFiles != 6 {
		t.Errorf("expected 6 files exported, got %d", res.TotalFiles)
	}

	// Inspect the generated archive
	manifest, err := recovery.Inspect(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("inspect failed: %v", err)
	}
	if manifest.FormatVersion != recovery.FormatVersion {
		t.Errorf("expected format version %d, got %d", recovery.FormatVersion, manifest.FormatVersion)
	}
	if len(manifest.Profiles) != 2 {
		t.Errorf("expected 2 profiles in manifest, got %d", len(manifest.Profiles))
	}
	if len(manifest.Files) != 6 {
		t.Errorf("expected 6 files in manifest, got %d", len(manifest.Files))
	}

	// Verify workspace history was sanitized
	va, err := recovery.ParseArchive(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("parse archive: %v", err)
	}
	wsData, ok := va.Files["data/users/guest/config/workspace.json"]
	if !ok {
		t.Fatal("workspace.json missing from archive")
	}
	if strings.Contains(string(wsData), "1234567890abcdef") {
		t.Errorf("workspace.json leaked auth command into backup: %s", string(wsData))
	}
}

func TestRestoreIntoFreshVFS(t *testing.T) {
	srcVFS := newTestVFS(t)
	populateTestData(t, srcVFS)

	buf := new(bytes.Buffer)
	_, err := recovery.Export(context.Background(), srcVFS, buf, recovery.ExportOptions{IncludeSystem: true})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	archiveBytes := buf.Bytes()

	// Restore into completely fresh VFS
	dstVFS := vfs.New(vfs.NewMem())
	rep, err := recovery.Restore(context.Background(), bytes.NewReader(archiveBytes), dstVFS, recovery.RestoreOptions{
		Strategy: recovery.ConflictAbort,
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if rep.RestoredCount != 6 {
		t.Errorf("expected 6 files restored, got %d", rep.RestoredCount)
	}

	// Verify content
	note, err := dstVFS.ReadFile("/users/guest/documents/notes.txt")
	if err != nil || string(note) != "Hello Gostalgia" {
		t.Fatalf("note content mismatch: %s, %v", string(note), err)
	}

	sysCfg, err := dstVFS.ReadFile("/config/system.json")
	if err != nil || string(sysCfg) != `{"theme":"nostalgia"}` {
		t.Fatalf("system config mismatch: %s, %v", string(sysCfg), err)
	}
}

func TestCredentialExclusionInExport(t *testing.T) {
	v := newTestVFS(t)
	populateTestData(t, v)

	secretToken := "very-sensitive-operator-token-998877"
	// Write a file that accidentally contains the secret token
	if err := v.WriteFile("/users/guest/documents/leak.txt", []byte("Token: "+secretToken), 0o644); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	opts := recovery.ExportOptions{
		ExcludeSecrets: []string{secretToken},
	}
	_, err := recovery.Export(context.Background(), v, buf, opts)
	if err == nil {
		t.Fatal("expected export to fail when file contains sensitive token, got nil")
	}
	if !strings.Contains(err.Error(), "contains sensitive credential") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestConflictDetectionAndStrategies(t *testing.T) {
	srcVFS := newTestVFS(t)
	populateTestData(t, srcVFS)

	buf := new(bytes.Buffer)
	_, err := recovery.Export(context.Background(), srcVFS, buf, recovery.ExportOptions{IncludeSystem: true})
	if err != nil {
		t.Fatal(err)
	}
	archiveData := buf.Bytes()

	// Destination VFS starts by restoring the clean archive, then modifies notes.txt
	dstVFS := newTestVFS(t)
	if _, err := recovery.Restore(context.Background(), bytes.NewReader(archiveData), dstVFS, recovery.RestoreOptions{
		Strategy: recovery.ConflictAbort,
	}); err != nil {
		t.Fatal(err)
	}
	if err := dstVFS.WriteFile("/users/guest/documents/notes.txt", []byte("Locally modified notes!"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Preview
	prev, err := recovery.Preview(bytes.NewReader(archiveData), dstVFS)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !prev.HasConflicts() {
		t.Fatal("expected preview to report conflicts")
	}
	if len(prev.Conflicts) != 1 || prev.Conflicts[0].VFSPath != "/users/guest/documents/notes.txt" {
		t.Fatalf("unexpected conflicts list: %+v", prev.Conflicts)
	}

	// 2. ConflictAbort (default) fails and does not modify data
	_, err = recovery.Restore(context.Background(), bytes.NewReader(archiveData), dstVFS, recovery.RestoreOptions{
		Strategy: recovery.ConflictAbort,
	})
	if err == nil {
		t.Fatal("expected restore with ConflictAbort to fail on conflict")
	}
	// Verify content was unchanged
	curr, _ := dstVFS.ReadFile("/users/guest/documents/notes.txt")
	if string(curr) != "Locally modified notes!" {
		t.Fatalf("conflict abort modified existing data: %s", string(curr))
	}

	// 3. ConflictSkip preserves the local file
	repSkip, err := recovery.Restore(context.Background(), bytes.NewReader(archiveData), dstVFS, recovery.RestoreOptions{
		Strategy: recovery.ConflictSkip,
	})
	if err != nil {
		t.Fatalf("restore with ConflictSkip failed: %v", err)
	}
	if repSkip.SkippedCount != 1 {
		t.Errorf("expected 1 file skipped, got %d", repSkip.SkippedCount)
	}
	curr, _ = dstVFS.ReadFile("/users/guest/documents/notes.txt")
	if string(curr) != "Locally modified notes!" {
		t.Fatalf("conflict skip modified existing data: %s", string(curr))
	}

	// 4. ConflictOverwrite replaces the file with archive content
	repOver, err := recovery.Restore(context.Background(), bytes.NewReader(archiveData), dstVFS, recovery.RestoreOptions{
		Strategy: recovery.ConflictOverwrite,
	})
	if err != nil {
		t.Fatalf("restore with ConflictOverwrite failed: %v", err)
	}
	if repOver.RestoredCount == 0 {
		t.Errorf("expected restored count > 0, got %d", repOver.RestoredCount)
	}
	curr, _ = dstVFS.ReadFile("/users/guest/documents/notes.txt")
	if string(curr) != "Hello Gostalgia" {
		t.Fatalf("conflict overwrite did not update content: %s", string(curr))
	}
}

func TestAdversarialArchivesRefused(t *testing.T) {
	dstVFS := newTestVFS(t)

	// Case 1: Malformed ZIP bytes
	_, err := recovery.Inspect(strings.NewReader("not a zip file at all"))
	if err == nil {
		t.Fatal("expected malformed archive to fail inspection")
	}

	// Helper to create an in-memory zip
	makeZip := func(files map[string][]byte) []byte {
		buf := new(bytes.Buffer)
		zw := zip.NewWriter(buf)
		for name, content := range files {
			w, _ := zw.Create(name)
			_, _ = w.Write(content)
		}
		_ = zw.Close()
		return buf.Bytes()
	}

	// Case 2: Missing backup.json
	noManifest := makeZip(map[string][]byte{
		"data/users/guest/documents/test.txt": []byte("foo"),
	})
	_, err = recovery.ParseArchive(bytes.NewReader(noManifest))
	if err == nil || !strings.Contains(err.Error(), "missing required backup.json") {
		t.Fatalf("expected missing backup.json error, got: %v", err)
	}

	// Case 3: Path traversal attempt
	traversalManifest := recovery.Manifest{
		FormatVersion: 1,
		CreatedAt:     time.Now(),
		Files: []recovery.FileEntry{
			{
				VFSPath: "/users/guest/documents/../../etc/passwd",
				ArcName: "data/users/guest/documents/../../etc/passwd",
				Size:    4,
				SHA256:  recovery.ComputeSHA256([]byte("root")),
			},
		},
	}
	mBytes, _ := json.Marshal(traversalManifest)
	traversalZip := makeZip(map[string][]byte{
		"backup.json": mBytes,
		"data/users/guest/documents/../../etc/passwd": []byte("root"),
	})
	_, err = recovery.ParseArchive(bytes.NewReader(traversalZip))
	if err == nil {
		t.Fatal("expected path traversal archive to be refused")
	}

	// Case 4: Forbidden file attempt (runtime.json)
	runtimeManifest := recovery.Manifest{
		FormatVersion: 1,
		CreatedAt:     time.Now(),
		Files: []recovery.FileEntry{
			{
				VFSPath: "/runtime.json",
				ArcName: "data/runtime.json",
				Size:    4,
				SHA256:  recovery.ComputeSHA256([]byte("auth")),
			},
		},
	}
	rmBytes, _ := json.Marshal(runtimeManifest)
	runtimeZip := makeZip(map[string][]byte{
		"backup.json":       rmBytes,
		"data/runtime.json": []byte("auth"),
	})
	_, err = recovery.ParseArchive(bytes.NewReader(runtimeZip))
	if err == nil {
		t.Fatal("expected runtime.json in archive to be refused")
	}
	if !strings.Contains(err.Error(), "forbidden VFS path") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Case 5: Unmanifested file injection
	validManifest := recovery.Manifest{
		FormatVersion: 1,
		CreatedAt:     time.Now(),
		Files: []recovery.FileEntry{
			{
				VFSPath: "/users/guest/documents/a.txt",
				ArcName: "data/users/guest/documents/a.txt",
				Size:    1,
				SHA256:  recovery.ComputeSHA256([]byte("a")),
			},
		},
	}
	vmBytes, _ := json.Marshal(validManifest)
	injectedZip := makeZip(map[string][]byte{
		"backup.json":                      vmBytes,
		"data/users/guest/documents/a.txt": []byte("a"),
		"data/users/guest/documents/b.txt": []byte("unmanifested sneaky file"),
	})
	_, err = recovery.ParseArchive(bytes.NewReader(injectedZip))
	if err == nil || !strings.Contains(err.Error(), "unmanifested file") {
		t.Fatalf("expected unmanifested file error, got: %v", err)
	}

	// Verify dstVFS was never modified
	if _, err := dstVFS.ReadFile("/users/guest/documents/a.txt"); err == nil {
		t.Fatal("destination VFS should remain clean")
	}
}

type failOnPathVFS struct {
	vfs.FS
	failPath string
}

func (f *failOnPathVFS) SaveAtomic(path string, data []byte, perm fs.FileMode) error {
	if path == f.failPath {
		return errors.New("simulated I/O failure during save")
	}
	return f.FS.SaveAtomic(path, data, perm)
}

func TestRestoreTransactionalRollback(t *testing.T) {
	baseVFS := newTestVFS(t)

	// Existing original file that would be overwritten
	origExisting := []byte("original preserved content")
	if err := baseVFS.WriteFile("/users/guest/documents/existing.txt", origExisting, 0o644); err != nil {
		t.Fatal(err)
	}

	// Prepare an archive that has:
	// 1. existing.txt (modified content)
	// 2. doomed.txt (new file that will fail during write)
	manifest := recovery.Manifest{
		FormatVersion: recovery.FormatVersion,
		CreatedAt:     time.Now(),
		Files: []recovery.FileEntry{
			{
				VFSPath: "/users/guest/documents/existing.txt",
				ArcName: "data/users/guest/documents/existing.txt",
				Size:    int64(len("new existing content")),
				SHA256:  recovery.ComputeSHA256([]byte("new existing content")),
			},
			{
				VFSPath: "/users/guest/documents/doomed.txt",
				ArcName: "data/users/guest/documents/doomed.txt",
				Size:    int64(len("doomed content")),
				SHA256:  recovery.ComputeSHA256([]byte("doomed content")),
			},
		},
	}
	mBytes, _ := json.Marshal(manifest)

	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	w, _ := zw.Create("backup.json")
	_, _ = w.Write(mBytes)
	w, _ = zw.Create("data/users/guest/documents/existing.txt")
	_, _ = w.Write([]byte("new existing content"))
	w, _ = zw.Create("data/users/guest/documents/doomed.txt")
	_, _ = w.Write([]byte("doomed content"))
	_ = zw.Close()

	failVFS := &failOnPathVFS{
		FS:       baseVFS,
		failPath: "/users/guest/documents/doomed.txt",
	}

	// Run restore with ConflictOverwrite
	_, err := recovery.Restore(context.Background(), bytes.NewReader(buf.Bytes()), failVFS, recovery.RestoreOptions{
		Strategy: recovery.ConflictOverwrite,
	})
	if err == nil {
		t.Fatal("expected restore to fail due to simulated I/O failure")
	}
	if !strings.Contains(err.Error(), "simulated I/O failure") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "rolled back successfully") {
		t.Fatalf("expected rollback success confirmation, got: %v", err)
	}

	// Verify rollback restored existing.txt to its original content
	existingData, err := baseVFS.ReadFile("/users/guest/documents/existing.txt")
	if err != nil {
		t.Fatalf("failed to read existing file: %v", err)
	}
	if string(existingData) != string(origExisting) {
		t.Fatalf("existing file not rolled back, got: %s", string(existingData))
	}

	// Verify doomed.txt was never created
	if _, err := baseVFS.ReadFile("/users/guest/documents/doomed.txt"); err == nil {
		t.Fatal("doomed.txt should not exist after rollback")
	}
}
