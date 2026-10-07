package services

// Regression tests for the document-handoff fixes in this branch. PoCs
// for the other audit findings have graduated upstream: session/profile
// capability gaps and the recents oracle live in this package's regular
// test files, the net/fetch redirect bypass in net_test.go, and the
// export/restore findings in recovery_audit_test.go.

import (
	"context"
	"encoding/json"
	"testing"

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
