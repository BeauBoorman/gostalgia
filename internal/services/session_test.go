package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gostalgia/internal/security"
	"gostalgia/internal/session"
)

func TestSessionService_LifecycleAndAttachments(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	caps := security.AdminCapabilities()

	// 1. List sessions: should include default guest session
	resp := env.call(ctx, caps, "session/list", nil)
	if !resp.OK {
		t.Fatalf("session/list failed: %s", resp.Error)
	}
	var list []SessionDetail
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	defaultSessID := list[0].ID

	// 2. Attach client 1
	resp = env.call(ctx, caps, "session/attach", map[string]any{
		"session_id":  defaultSessID,
		"client_type": "shell",
		"client_id":   "shell-1",
	})
	if !resp.OK {
		t.Fatalf("session/attach failed: %s", resp.Error)
	}
	var attResp1 SessionAttachResponse
	if err := json.Unmarshal(resp.Data, &attResp1); err != nil {
		t.Fatal(err)
	}
	if attResp1.AttachmentID == "" {
		t.Fatal("empty attachment id")
	}
	if attResp1.ActiveCount != 1 {
		t.Fatalf("expected active_count=1, got %d", attResp1.ActiveCount)
	}
	if attResp1.Workspace.CurrentDir == "" {
		t.Fatal("workspace current dir is empty")
	}

	// 3. Attach client 2 (concurrent multi-client)
	resp = env.call(ctx, caps, "session/attach", map[string]any{
		"session_id":  defaultSessID,
		"client_type": "gctl",
		"client_id":   "gctl-2",
	})
	if !resp.OK {
		t.Fatalf("session/attach client 2 failed: %s", resp.Error)
	}
	var attResp2 SessionAttachResponse
	if err := json.Unmarshal(resp.Data, &attResp2); err != nil {
		t.Fatal(err)
	}
	if attResp2.ActiveCount != 2 {
		t.Fatalf("expected active_count=2, got %d", attResp2.ActiveCount)
	}

	// 4. Detach client 1
	resp = env.call(ctx, caps, "session/detach", map[string]any{
		"session_id":    defaultSessID,
		"attachment_id": attResp1.AttachmentID,
	})
	if !resp.OK {
		t.Fatalf("session/detach failed: %s", resp.Error)
	}
	var detachRes struct {
		Detached    bool `json:"detached"`
		ActiveCount int  `json:"active_count"`
	}
	if err := json.Unmarshal(resp.Data, &detachRes); err != nil {
		t.Fatal(err)
	}
	if !detachRes.Detached || detachRes.ActiveCount != 1 {
		t.Fatalf("expected detached=true, count=1, got %+v", detachRes)
	}

	// Session must still be active and reachable by client 2
	resp = env.call(ctx, caps, "session/get", map[string]any{"id": defaultSessID})
	if !resp.OK {
		t.Fatalf("session/get failed: %s", resp.Error)
	}
	var sessDetail SessionDetail
	if err := json.Unmarshal(resp.Data, &sessDetail); err != nil {
		t.Fatal(err)
	}
	if !sessDetail.Active {
		t.Fatal("session is unexpectedly inactive")
	}
	if len(sessDetail.Attachments) != 1 || sessDetail.Attachments[0].ID != attResp2.AttachmentID {
		t.Fatalf("unexpected remaining attachments: %+v", sessDetail.Attachments)
	}

	// 5. Detach client 2
	resp = env.call(ctx, caps, "session/detach", map[string]any{
		"session_id":    defaultSessID,
		"attachment_id": attResp2.AttachmentID,
	})
	if !resp.OK {
		t.Fatalf("session/detach client 2 failed: %s", resp.Error)
	}

	// Verify session remains open even with 0 attached clients (headless)
	resp = env.call(ctx, caps, "session/get", map[string]any{"id": defaultSessID})
	if !resp.OK {
		t.Fatalf("session/get failed: %s", resp.Error)
	}
	if err := json.Unmarshal(resp.Data, &sessDetail); err != nil {
		t.Fatal(err)
	}
	if !sessDetail.Active {
		t.Fatal("session should remain active when all clients detach")
	}
	if len(sessDetail.Attachments) != 0 {
		t.Fatalf("expected 0 attachments, got %d", len(sessDetail.Attachments))
	}
}

func TestSessionService_WorkspacePersistence(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	caps := security.AdminCapabilities()

	// Update workspace state
	resp := env.call(ctx, caps, "session/workspace/set", map[string]any{
		"cwd":         "/users/guest/documents",
		"active_view": "launcher",
		"cmd":         "open doc.txt",
	})
	if !resp.OK {
		t.Fatalf("workspace/set failed: %s", resp.Error)
	}
	var ws session.WorkspaceState
	if err := json.Unmarshal(resp.Data, &ws); err != nil {
		t.Fatal(err)
	}
	if ws.CurrentDir != "/users/guest/documents" {
		t.Fatalf("cwd = %q, want /users/guest/documents", ws.CurrentDir)
	}
	if ws.ActiveView != "launcher" {
		t.Fatalf("active_view = %q, want launcher", ws.ActiveView)
	}
	if len(ws.History) != 1 || ws.History[0] != "open doc.txt" {
		t.Fatalf("history = %+v, want ['open doc.txt']", ws.History)
	}

	// Append sensitive command: must be excluded or sanitized
	resp = env.call(ctx, caps, "session/workspace/set", map[string]any{
		"cmd": "auth login token 0123456789abcdef0123456789abcdef",
	})
	if !resp.OK {
		t.Fatalf("workspace/set sensitive failed: %s", resp.Error)
	}
	if err := json.Unmarshal(resp.Data, &ws); err != nil {
		t.Fatal(err)
	}
	for _, h := range ws.History {
		if strings.Contains(h, "0123456789abcdef0123456789abcdef") {
			t.Fatalf("raw token leaked into workspace history: %q", h)
		}
	}

	// Fetch workspace state
	resp = env.call(ctx, caps, "session/workspace/get", nil)
	if !resp.OK {
		t.Fatalf("workspace/get failed: %s", resp.Error)
	}
	if err := json.Unmarshal(resp.Data, &ws); err != nil {
		t.Fatal(err)
	}
	if ws.CurrentDir != "/users/guest/documents" {
		t.Fatalf("fetched cwd = %q, want /users/guest/documents", ws.CurrentDir)
	}

	// Clear history
	resp = env.call(ctx, caps, "session/workspace/clear", map[string]any{
		"clear_history": true,
	})
	if !resp.OK {
		t.Fatalf("workspace/clear failed: %s", resp.Error)
	}
	var wsCleared session.WorkspaceState
	if err := json.Unmarshal(resp.Data, &wsCleared); err != nil {
		t.Fatal(err)
	}
	if len(wsCleared.History) != 0 {
		t.Fatalf("expected empty history after clear, got %v", wsCleared.History)
	}
}

func TestSessionService_Permissions(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Missing capabilities must fail
	noCaps := security.NewCapabilities()
	resp := env.call(ctx, noCaps, "session/list", nil)
	if resp.OK {
		t.Fatal("session/list without permissions succeeded, want error")
	}

	resp = env.call(ctx, noCaps, "session/attach", nil)
	if resp.OK {
		t.Fatal("session/attach without permissions succeeded, want error")
	}

	resp = env.call(ctx, noCaps, "session/workspace/get", nil)
	if resp.OK {
		t.Fatal("session/workspace/get without permissions succeeded, want error")
	}

	// With CapSessionRead
	readCaps := security.NewCapabilities(security.CapSessionRead)
	resp = env.call(ctx, readCaps, "session/list", nil)
	if !resp.OK {
		t.Fatalf("session/list with CapSessionRead failed: %s", resp.Error)
	}

	// The baseline CapIPC alone is not enough (#73).
	ipcCaps := security.NewCapabilities(security.CapIPC)
	resp = env.call(ctx, ipcCaps, "session/list", nil)
	if resp.OK {
		t.Fatal("session/list with only CapIPC succeeded, want error")
	}
}

// #73: app principals may only address the session they are bound to.
func TestSessionService_AppScopedToOwnSession(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	resp := env.call(ctx, security.AdminCapabilities(), "session/list", nil)
	if !resp.OK {
		t.Fatalf("session/list failed: %s", resp.Error)
	}
	var list []SessionDetail
	must(t, json.Unmarshal(resp.Data, &list))
	if len(list) == 0 {
		t.Fatal("no sessions")
	}
	operatorSessID := list[0].ID

	appCaps := security.NewCapabilities(security.CapIPC, security.CapSessionRead, security.CapSessionWrite)

	// An app bound to a different session cannot read, mutate, or close the
	// operator's session even with session capabilities.
	foreignApp := security.AppPrincipal("com.test.foreign", 7, "session-does-not-exist", security.User{ID: "u-1", Name: "appuser"})
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"session/get", map[string]any{"id": operatorSessID}},
		{"session/close", map[string]any{"id": operatorSessID}},
		{"session/workspace/get", map[string]any{"session_id": operatorSessID}},
		{"session/workspace/set", map[string]any{"session_id": operatorSessID, "cmd": "evil"}},
		{"session/attach", map[string]any{"session_id": operatorSessID, "client_type": "shell"}},
		{"session/detach", map[string]any{"session_id": operatorSessID, "attachment_id": "att-1"}},
	} {
		if resp := env.callAs(ctx, foreignApp, appCaps, tc.method, tc.params); resp.OK {
			t.Fatalf("%s by foreign-bound app succeeded, want error", tc.method)
		}
	}

	// An app with no bound session cannot reach the implicit current session.
	unboundApp := security.AppPrincipal("com.test.unbound", 8, "", security.User{ID: "u-1", Name: "appuser"})
	if resp := env.callAs(ctx, unboundApp, appCaps, "session/workspace/get", nil); resp.OK {
		t.Fatal("unbound app resolved the operator's implicit session")
	}
	if resp := env.callAs(ctx, unboundApp, appCaps, "session/list", nil); !resp.OK {
		t.Fatalf("session/list failed for unbound app: %s", resp.Error)
	} else {
		var appList []SessionDetail
		must(t, json.Unmarshal(resp.Data, &appList))
		if len(appList) != 0 {
			t.Fatalf("unbound app listed %d sessions, want 0", len(appList))
		}
	}

	// An app bound to a real session can use it.
	resp = env.call(ctx, security.AdminCapabilities(), "session/create", map[string]any{"user_id": "u-1", "user_name": "appuser"})
	if !resp.OK {
		t.Fatalf("session/create failed: %s", resp.Error)
	}
	var created SessionDetail
	must(t, json.Unmarshal(resp.Data, &created))
	boundApp := security.AppPrincipal("com.test.bound", 9, created.ID, security.User{ID: "u-1", Name: "appuser"})
	if resp := env.callAs(ctx, boundApp, appCaps, "session/workspace/set", map[string]any{"cmd": "ls"}); !resp.OK {
		t.Fatalf("bound app could not write own workspace: %s", resp.Error)
	}

	// An app cannot mint a session for a different user identity.
	if resp := env.callAs(ctx, boundApp, appCaps, "session/create", map[string]any{"user_id": "root"}); resp.OK {
		t.Fatal("app created a session for another user")
	}
}

// #73: session/* must not accept the baseline ipc cap — an app with only
// 'ipc' can no longer inject into the user's persistent shell history.
// (PoC: internal/services/audit_findings_test.go on the audit branch.)
func TestAuditSessionHistoryInjection(t *testing.T) {
	env := newTestEnv(t)
	evilApp := security.AppPrincipal("com.test.evil", 7, "", security.User{Name: "guest"})
	ipcOnly := security.NewCapabilities(security.CapIPC)
	resp := env.callAs(context.Background(), evilApp, ipcOnly,
		"session/workspace/set", map[string]any{"cmd": "open /users/guest/documents/diary.txt"})
	if resp.OK {
		t.Fatal("workspace/set accepted an app with only 'ipc' cap")
	}
	// The operator's workspace history must be untouched.
	resp = env.call(context.Background(), security.AdminCapabilities(), "session/workspace/get", nil)
	if !resp.OK {
		t.Fatalf("workspace/get failed: %s", resp.Error)
	}
	var ws struct {
		History []string `json:"history"`
	}
	must(t, json.Unmarshal(resp.Data, &ws))
	for _, h := range ws.History {
		if strings.Contains(h, "diary.txt") {
			t.Fatal("injected command found in workspace history")
		}
	}
}

// #73: session routes require session.read/session.write (or operator), not
// the baseline ipc cap every app holds.
func TestAuditSessionCloseByApp(t *testing.T) {
	env := newTestEnv(t)
	evilApp := security.AppPrincipal("com.test.evil", 7, "", security.User{Name: "guest"})
	ipcOnly := security.NewCapabilities(security.CapIPC)
	// discover a session id as operator
	resp := env.call(context.Background(), security.AdminCapabilities(), "session/list", nil)
	var list []struct {
		ID string `json:"id"`
	}
	must(t, json.Unmarshal(resp.Data, &list))
	if len(list) == 0 {
		t.Fatal("no sessions")
	}
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"session/list", nil},
		{"session/close", map[string]any{"id": list[0].ID}},
		{"session/create", map[string]any{"user_id": "attacker"}},
	} {
		resp = env.callAs(context.Background(), evilApp, ipcOnly, tc.method, tc.params)
		if resp.OK {
			t.Fatalf("%s accepted an app with only 'ipc' cap", tc.method)
		}
	}
	// The operator session is still open.
	resp = env.call(context.Background(), security.AdminCapabilities(), "session/get", map[string]any{"id": list[0].ID})
	if !resp.OK {
		t.Fatalf("operator session is gone: %s", resp.Error)
	}
}
