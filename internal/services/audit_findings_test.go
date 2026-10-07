package services

// Audit proof-of-trigger tests for findings that are still open, plus
// regression tests for the fixes in this branch. Findings already fixed
// upstream have graduated into the package's regular test files
// (session_test.go, profile_test.go, document_test.go, net_test.go).

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gostalgia/internal/recovery"
	"gostalgia/internal/security"
	"gostalgia/internal/vfs"
	"gostalgia/sdk"
)

var (
	evilApp   = security.AppPrincipal("com.test.evil", 7, "", security.User{Name: "guest"})
	ipcOnly   = security.NewCapabilities(security.CapIPC)
	adminCaps = security.AdminCapabilities()
)

// Regression for #75: doc/associations/register requires the admin
// capability, and applications can never claim a default handler — a
// hijacked default silently receives a scoped grant for every matching
// document the operator opens.
func TestAuditAssociationHijackGrant(t *testing.T) {
	env := newTestEnv(t)
	// operator writes a private document
	must(t, env.ctx.VFS.MkdirAll("/users/guest/documents"))
	must(t, env.ctx.VFS.WriteFile("/users/guest/documents/secret.md", []byte("TOP SECRET"), 0o644))

	// An app with only the ipc capability cannot register at all.
	resp := env.callAs(context.Background(), evilApp, ipcOnly,
		"doc/associations/register", map[string]any{
			"extension": ".md", "app_id": "com.test.evil", "name": "Evil", "default": true,
		})
	if resp.OK {
		t.Fatal("associations/register accepted an uncapable app caller")
	}

	// Even an admin-capable app cannot claim Default: defaults are an
	// operator decision. Non-default self-registration is still denied
	// by the capability check above; re-check with admin caps to isolate
	// the operator-only default rule.
	resp = env.callAs(context.Background(), evilApp, adminCaps,
		"doc/associations/register", map[string]any{
			"extension": ".md", "app_id": "com.test.evil", "name": "Evil", "default": true,
		})
	if resp.OK {
		t.Fatal("app principal claimed a default association")
	}

	// operator opens the doc; handoff resolves the builtin default and the
	// hijacker must receive nothing.
	resp = env.call(context.Background(), adminCaps, "doc/handoff", map[string]any{
		"version": sdk.DocumentHandoffVersion, "path": "/users/guest/documents/secret.md",
	})
	if !resp.OK {
		t.Fatalf("handoff failed: %s", resp.Error)
	}
	var res sdk.HandoffResult
	must(t, json.Unmarshal(resp.Data, &res))
	if res.AppID == "com.test.evil" {
		t.Fatal("handoff resolved the hijacked default app")
	}
	gs := env.ctx.VFS.(*vfs.VFS).Grants()
	if err := gs.CheckAccess("com.test.evil", "/users/guest/documents/secret.md", vfs.AccessRead); err == nil {
		t.Fatal("hijacker received a grant for the operator-opened document")
	}
}

// Regression for #76: a failed handoff must not leave a live grant. The
// grant is issued only after the target launches, and revoked if dispatch
// fails.
func TestAuditHandoffGrantLeakOnFailure(t *testing.T) {
	env := newTestEnv(t)
	must(t, env.ctx.VFS.MkdirAll("/users/guest/documents"))
	must(t, env.ctx.VFS.WriteFile("/users/guest/documents/f.txt", []byte("x"), 0o644))

	resp := env.call(context.Background(), adminCaps, "doc/handoff", map[string]any{
		"version": sdk.DocumentHandoffVersion, "path": "/users/guest/documents/f.txt",
		"app_id": "com.test.nonexistent",
	})
	if resp.OK {
		t.Fatal("handoff to nonexistent app unexpectedly succeeded")
	}
	gs := env.ctx.VFS.(*vfs.VFS).Grants()
	for _, g := range gs.List("com.test.nonexistent") {
		if !g.Revoked {
			t.Fatalf("failed handoff left a live grant: %+v", g)
		}
	}
	if err := gs.CheckAccess("com.test.nonexistent", "/users/guest/documents/f.txt", vfs.AccessRead); err == nil {
		t.Fatal("failed handoff left a live grant to a nonexistent app")
	}
}

// F: backup export silently drops files — a user's *.zip documents, unreadable
// files, and anything under /apps/data never make the archive, yet the export
// reports success.
func TestAuditExportSilentlyDropsUserFiles(t *testing.T) {
	env := newTestEnv(t)
	must(t, env.ctx.VFS.MkdirAll("/users/guest/documents"))
	must(t, env.ctx.VFS.WriteFile("/users/guest/documents/keep.txt", []byte("keep me"), 0o644))
	must(t, env.ctx.VFS.WriteFile("/users/guest/documents/archive.zip", []byte("PK fake zip"), 0o644))
	must(t, env.ctx.VFS.WriteFile("/users/guest/documents/photo.gbar", []byte("not a backup"), 0o644))

	var buf bytes.Buffer
	res, err := recovery.Export(context.Background(), env.ctx.VFS, &buf, recovery.ExportOptions{})
	must(t, err)
	t.Logf("export reported success: %d files", res.TotalFiles)
	for _, f := range resArchiveFiles(t, buf.Bytes()) {
		if strings.HasSuffix(f, "archive.zip") || strings.HasSuffix(f, "photo.gbar") {
			t.Errorf("unexpected file exported: %s", f)
		}
	}
	if !strings.Contains(strings.Join(resArchiveFiles(t, buf.Bytes()), ","), "keep.txt") {
		t.Error("keep.txt missing from export")
	}
	t.Log("CONFIRMED: user files archive.zip/photo.gbar silently absent from a 'successful' backup")
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

// F: restore treats any live ReadFile error as "file does not exist", so an
// unreadable existing file is planned as a create — overwritten with no
// conflict check and no rollback journal entry for its content.
func TestAuditRestoreOverwritesUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 does not make a file unreadable on Windows")
	}
	env := newTestEnv(t)
	target := "/users/guest/documents/locked.txt"
	hostTarget := filepath.Join(env.ctx.Root, "vfs", "users/guest/documents/locked.txt")
	must(t, env.ctx.VFS.MkdirAll("/users/guest/documents"))
	must(t, env.ctx.VFS.WriteFile(target, []byte("ORIGINAL-SECRET"), 0o644))
	must(t, os.Chmod(hostTarget, 0o000))
	t.Cleanup(func() { _ = os.Chmod(hostTarget, 0o644) })

	// Build an archive containing the same path with different content.
	archive := buildArchive(t, "/users/guest/documents/locked.txt", "REPLACED")
	rep, err := recovery.Restore(context.Background(), bytes.NewReader(archive), env.ctx.VFS,
		recovery.RestoreOptions{Strategy: recovery.ConflictAbort})
	if err != nil {
		t.Fatalf("restore failed instead of overwriting or refusing: %v", err)
	}
	_ = rep
	_ = os.Chmod(hostTarget, 0o644)
	got, err := env.ctx.VFS.ReadFile(target)
	must(t, err)
	if string(got) != "REPLACED" {
		t.Fatalf("content = %q, want REPLACED — abort strategy must refuse", got)
	}
	t.Log("CONFIRMED: unreadable live file silently overwritten under 'abort' strategy")
}

// F: restore's profile filter only filters /users/* paths — /config/* files
// are written regardless, so a "profile-scoped" restore overwrites global state.
func TestAuditRestoreProfileFilterBypass(t *testing.T) {
	env := newTestEnv(t)
	must(t, env.ctx.VFS.WriteFile("/config/system.json", []byte(`{"original":true}`), 0o644))
	archive := buildArchive(t, "/config/system.json", `{"attacker":true}`)
	_, err := recovery.Restore(context.Background(), bytes.NewReader(archive), env.ctx.VFS,
		recovery.RestoreOptions{Strategy: recovery.ConflictOverwrite, ProfileFilter: "nonexistent-profile"})
	must(t, err)
	got, err := env.ctx.VFS.ReadFile("/config/system.json")
	must(t, err)
	if !strings.Contains(string(got), "attacker") {
		t.Fatal("system.json not overwritten")
	}
	t.Log("CONFIRMED: profile-filtered restore wrote /config/* anyway")
}

// buildArchive produces a minimal valid .gbar with one manifest entry.
func buildArchive(t *testing.T, vfsPath, content string) []byte {
	t.Helper()
	arcName := recovery.DataDirPrefix + strings.TrimPrefix(vfsPath, "/")
	man := recovery.Manifest{
		FormatVersion: recovery.FormatVersion,
		Files: []recovery.FileEntry{{
			VFSPath: vfsPath, ArcName: arcName,
			Size: int64(len(content)), SHA256: recovery.ComputeSHA256([]byte(content)),
		}},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	manifestBytes, err := json.Marshal(man)
	must(t, err)
	for name, body := range map[string][]byte{
		recovery.ManifestFilename: manifestBytes,
		arcName:                   []byte(content),
	} {
		w, err := zw.Create(name)
		must(t, err)
		_, err = w.Write(body)
		must(t, err)
	}
	must(t, zw.Close())
	return buf.Bytes()
}
