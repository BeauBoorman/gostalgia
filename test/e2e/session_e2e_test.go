package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"gostalgia/internal/runtime"
	"gostalgia/internal/session"
)

func TestSessionDetachAndWorkspacePersistenceE2E(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("session e2e test") })

	client1 := dialRunning(t, root)
	defer client1.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Client 1 attaches
	var att1 struct {
		SessionID    string                 `json:"session_id"`
		AttachmentID string                 `json:"attachment_id"`
		ActiveCount  int                    `json:"active_count"`
		Workspace    session.WorkspaceState `json:"workspace"`
	}
	must(t, client1.Call(ctx, "session/attach", map[string]any{"client_type": "shell"}, &att1))
	if att1.SessionID == "" || att1.AttachmentID == "" {
		t.Fatalf("expected non-empty session and attachment IDs: %+v", att1)
	}
	if att1.ActiveCount != 1 {
		t.Fatalf("expected 1 active client, got %d", att1.ActiveCount)
	}
	if att1.Workspace.CurrentDir != "/users/guest" {
		t.Fatalf("expected initial cwd /users/guest, got %s", att1.Workspace.CurrentDir)
	}

	// 2. Client 1 updates workspace (CWD, view, and sensitive command)
	must(t, client1.Call(ctx, "session/workspace/set", map[string]any{
		"cwd":         "/users/guest/documents",
		"active_view": "apps",
		"cmd":         "login token=**************************************",
	}, nil))

	must(t, client1.Call(ctx, "session/workspace/set", map[string]any{
		"cmd": "curl --token secrettoken12345678901234567890",
	}, nil))

	must(t, client1.Call(ctx, "session/workspace/set", map[string]any{
		"cmd": "dir",
	}, nil))

	// 3. Client 2 attaches concurrently
	client2 := dialRunning(t, root)
	defer client2.Close()

	var att2 struct {
		SessionID    string                 `json:"session_id"`
		AttachmentID string                 `json:"attachment_id"`
		ActiveCount  int                    `json:"active_count"`
		Workspace    session.WorkspaceState `json:"workspace"`
	}
	must(t, client2.Call(ctx, "session/attach", map[string]any{"client_type": "shell"}, &att2))
	if att2.SessionID != att1.SessionID {
		t.Fatalf("expected same session ID %s, got %s", att1.SessionID, att2.SessionID)
	}
	if att2.ActiveCount != 2 {
		t.Fatalf("expected 2 active clients, got %d", att2.ActiveCount)
	}
	// Verify workspace state synced to Client 2
	if att2.Workspace.CurrentDir != "/users/guest/documents" {
		t.Fatalf("expected cwd /users/guest/documents, got %s", att2.Workspace.CurrentDir)
	}
	if att2.Workspace.ActiveView != "apps" {
		t.Fatalf("expected active_view apps, got %s", att2.Workspace.ActiveView)
	}
	// Verify sensitive command sanitization in history
	hasRedacted := false
	for _, cmd := range att2.Workspace.History {
		if strings.Contains(cmd, "secrettoken12345678901234567890") {
			t.Fatalf("sensitive token leaked in history: %s", cmd)
		}
		if strings.Contains(cmd, "login") {
			t.Fatalf("auth command should not be recorded in history: %s", cmd)
		}
		if strings.Contains(cmd, "[REDACTED]") {
			hasRedacted = true
		}
	}
	if !hasRedacted {
		t.Fatalf("expected [REDACTED] in sanitized history, got: %v", att2.Workspace.History)
	}

	// 4. Client 1 detaches cleanly
	var detResp struct {
		Detached bool `json:"detached"`
	}
	must(t, client1.Call(ctx, "session/detach", map[string]any{
		"session_id":    att1.SessionID,
		"attachment_id": att1.AttachmentID,
	}, &detResp))
	if !detResp.Detached {
		t.Fatal("expected detached to be true")
	}

	// Verify runtime is still running and Client 2 can inspect session
	var detail struct {
		ID          string `json:"id"`
		Active      bool   `json:"active"`
		Attachments []any  `json:"attachments"`
	}
	must(t, client2.Call(ctx, "session/get", nil, &detail))
	if !detail.Active {
		t.Fatal("expected session to remain active after client 1 detach")
	}
	if len(detail.Attachments) != 1 {
		t.Fatalf("expected 1 remaining attachment, got %d", len(detail.Attachments))
	}

	// 5. Client 2 detaches
	must(t, client2.Call(ctx, "session/detach", map[string]any{
		"session_id":    att2.SessionID,
		"attachment_id": att2.AttachmentID,
	}, nil))

	// 6. Client 3 attaches after zero clients were attached
	client3 := dialRunning(t, root)
	defer client3.Close()

	var att3 struct {
		SessionID   string                 `json:"session_id"`
		ActiveCount int                    `json:"active_count"`
		Workspace   session.WorkspaceState `json:"workspace"`
	}
	must(t, client3.Call(ctx, "session/attach", map[string]any{"client_type": "shell"}, &att3))
	if att3.SessionID != att1.SessionID {
		t.Fatalf("expected reattachment to session %s, got %s", att1.SessionID, att3.SessionID)
	}
	if att3.ActiveCount != 1 {
		t.Fatalf("expected 1 active client, got %d", att3.ActiveCount)
	}
	if att3.Workspace.CurrentDir != "/users/guest/documents" {
		t.Fatalf("expected persisted cwd /users/guest/documents, got %s", att3.Workspace.CurrentDir)
	}
}
