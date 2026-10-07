package services

// Regression tests for backup/restore integrity findings (issues #87, #88, #89).

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gostalgia/internal/recovery"
)

// #87: export silently dropped files — user *.zip documents, unreadable files,
// and /apps/data never made the archive while the export reported success.
// Fixed: ordinary user files are exported and every omission is enumerated in
// the export result.
func TestAuditExportSilentlyDropsUserFiles(t *testing.T) {
	env := newTestEnv(t)
	must(t, env.ctx.VFS.MkdirAll("/users/guest/documents"))
	must(t, env.ctx.VFS.WriteFile("/users/guest/documents/keep.txt", []byte("keep me"), 0o644))
	must(t, env.ctx.VFS.WriteFile("/users/guest/documents/archive.zip", []byte("PK fake zip"), 0o644))
	must(t, env.ctx.VFS.WriteFile("/users/guest/documents/photo.gbar", []byte("not a backup"), 0o644))
	must(t, env.ctx.VFS.WriteFile("/users/guest/documents/locked.txt", []byte("cannot read"), 0o644))
	// App-private data lives outside the portable backup scope by policy.
	must(t, env.ctx.VFS.MkdirAll("/apps/data/com.test.app"))
	must(t, env.ctx.VFS.WriteFile("/apps/data/com.test.app/state.json", []byte("{}"), 0o644))

	hostLocked := filepath.Join(env.ctx.Root, "vfs", "users/guest/documents/locked.txt")
	must(t, os.Chmod(hostLocked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(hostLocked, 0o644) })

	var buf bytes.Buffer
	res, err := recovery.Export(context.Background(), env.ctx.VFS, &buf, recovery.ExportOptions{})
	must(t, err)

	names := strings.Join(resArchiveFiles(t, buf.Bytes()), ",")
	if !strings.Contains(names, "keep.txt") {
		t.Error("keep.txt missing from export")
	}
	if !strings.Contains(names, "archive.zip") {
		t.Error("archive.zip is ordinary user data and must be exported")
	}
	if strings.Contains(names, "photo.gbar") {
		t.Error("backup-format archive files must not be re-exported")
	}

	// Every omission must be enumerated — no silent loss.
	skipReasons := map[string]string{}
	for _, s := range res.Skipped {
		skipReasons[s.Path] = s.Reason
	}
	for _, want := range []string{
		"/users/guest/documents/photo.gbar",
		"/users/guest/documents/locked.txt",
		"/apps/data",
	} {
		if _, ok := skipReasons[want]; !ok {
			t.Errorf("omission of %q not reported; skipped=%+v", want, res.Skipped)
		}
	}
}

func resArchiveFiles(t *testing.T, data []byte) []string {
	t.Helper()
	m, err := recovery.Inspect(bytes.NewReader(data))
	must(t, err)
	var names []string
	for _, f := range m.Files {
		names = append(names, f.VFSPath)
	}
	return names
}

// #88: restore treated any live ReadFile error as "file does not exist", so an
// unreadable existing file was planned as a create — overwritten with no
// conflict check and no rollback journal entry for its content. Fixed:
// non-NotExist read errors hard-fail under every conflict strategy.
func TestAuditRestoreOverwritesUnreadableFile(t *testing.T) {
	env := newTestEnv(t)
	target := "/users/guest/documents/locked.txt"
	hostTarget := filepath.Join(env.ctx.Root, "vfs", "users/guest/documents/locked.txt")
	must(t, env.ctx.VFS.MkdirAll("/users/guest/documents"))
	must(t, env.ctx.VFS.WriteFile(target, []byte("ORIGINAL-SECRET"), 0o644))
	must(t, os.Chmod(hostTarget, 0o000))
	t.Cleanup(func() { _ = os.Chmod(hostTarget, 0o644) })

	archive := buildArchive(t, target, "REPLACED")

	// Preview cannot classify the live file either — it must fail loudly.
	if _, err := recovery.Preview(bytes.NewReader(archive), env.ctx.VFS); err == nil {
		t.Error("preview classified an unreadable live file as a create")
	}

	for _, strategy := range []recovery.ConflictStrategy{
		recovery.ConflictAbort, recovery.ConflictSkip, recovery.ConflictOverwrite,
	} {
		if _, err := recovery.Restore(context.Background(), bytes.NewReader(archive), env.ctx.VFS,
			recovery.RestoreOptions{Strategy: strategy}); err == nil {
			t.Errorf("strategy %s: restore succeeded over an unreadable live file", strategy)
		}
	}

	_ = os.Chmod(hostTarget, 0o644)
	got, err := env.ctx.VFS.ReadFile(target)
	must(t, err)
	if string(got) != "ORIGINAL-SECRET" {
		t.Fatalf("content = %q, want ORIGINAL-SECRET preserved", got)
	}
}

// #89: restore's profile filter only filtered /users/* paths — /config/* files
// were written regardless, so a "profile-scoped" restore overwrote global
// state. Fixed: with ProfileFilter set, every entry outside /users/<profile>/
// is skipped and counted.
func TestAuditRestoreProfileFilterBypass(t *testing.T) {
	env := newTestEnv(t)
	must(t, env.ctx.VFS.WriteFile("/config/system.json", []byte(`{"original":true}`), 0o644))
	archive := buildArchiveMulti(t, map[string]string{
		"/config/system.json":                `{"attacker":true}`,
		"/users/guest/documents/note.txt":    "hello",
		"/users/other/documents/private.txt": "not yours",
	})
	rep, err := recovery.Restore(context.Background(), bytes.NewReader(archive), env.ctx.VFS,
		recovery.RestoreOptions{Strategy: recovery.ConflictOverwrite, ProfileFilter: "guest"})
	must(t, err)
	if rep.RestoredCount != 1 {
		t.Errorf("RestoredCount = %d, want 1 (only the guest file)", rep.RestoredCount)
	}
	if rep.SkippedCount != 2 {
		t.Errorf("SkippedCount = %d, want 2 (config + other profile filtered out)", rep.SkippedCount)
	}
	got, err := env.ctx.VFS.ReadFile("/config/system.json")
	must(t, err)
	if strings.Contains(string(got), "attacker") {
		t.Fatal("profile-filtered restore overwrote /config/*")
	}
	if _, err := env.ctx.VFS.Stat("/users/other/documents/private.txt"); err == nil {
		t.Fatal("profile-filtered restore wrote another profile's files")
	}
	if got, err := env.ctx.VFS.ReadFile("/users/guest/documents/note.txt"); err != nil || string(got) != "hello" {
		t.Fatalf("profile's own file not restored: %q, %v", got, err)
	}
}

// buildArchive produces a minimal valid .gbar with one manifest entry.
func buildArchive(t *testing.T, vfsPath, content string) []byte {
	t.Helper()
	return buildArchiveMulti(t, map[string]string{vfsPath: content})
}

// buildArchiveMulti produces a minimal valid .gbar from a vfsPath -> content map.
func buildArchiveMulti(t *testing.T, files map[string]string) []byte {
	t.Helper()
	man := recovery.Manifest{FormatVersion: recovery.FormatVersion}
	payloads := map[string][]byte{}
	for vfsPath, content := range files {
		arcName := recovery.DataDirPrefix + strings.TrimPrefix(vfsPath, "/")
		man.Files = append(man.Files, recovery.FileEntry{
			VFSPath: vfsPath, ArcName: arcName,
			Size: int64(len(content)), SHA256: recovery.ComputeSHA256([]byte(content)),
		})
		payloads[arcName] = []byte(content)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	manifestBytes, err := json.Marshal(man)
	must(t, err)
	payloads[recovery.ManifestFilename] = manifestBytes
	for name, body := range payloads {
		w, err := zw.Create(name)
		must(t, err)
		_, err = w.Write(body)
		must(t, err)
	}
	must(t, zw.Close())
	return buf.Bytes()
}
