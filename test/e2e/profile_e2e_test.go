package e2e

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"gostalgia/internal/process"
	"gostalgia/internal/runtime"
	"gostalgia/sdk"
)

func TestProfileLifecycleAndWorkspaceIsolationE2E(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("profile e2e test") })

	client := dialRunning(t, root)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Initial active profile is "guest"
	var activeResp struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	must(t, client.Call(ctx, "profile/active", nil, &activeResp))
	if activeResp.ID != "guest" {
		t.Fatalf("expected initial profile guest, got %s", activeResp.ID)
	}

	// Verify sys/status reports user: guest
	var statusResp struct {
		User string `json:"user"`
	}
	must(t, client.Call(ctx, "sys/status", nil, &statusResp))
	if statusResp.User != "guest" {
		t.Fatalf("expected sys/status user guest, got %s", statusResp.User)
	}

	// 2. Create new profile "alice"
	var createResp struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	must(t, client.Call(ctx, "profile/create", map[string]any{
		"id":          "alice",
		"name":        "Alice Engineer",
		"description": "Engineering Workspace",
		"preferences": map[string]any{
			"terminal.theme": "midnight",
		},
	}, &createResp))
	if createResp.ID != "alice" || createResp.Name != "Alice Engineer" {
		t.Fatalf("unexpected profile create response: %+v", createResp)
	}

	// Verify Alice's directories are seeded under /users/alice
	for _, sub := range []string{"documents", "downloads", "desktop", "config", ".trash"} {
		var stat struct {
			Path  string `json:"path"`
			IsDir bool   `json:"is_dir"`
		}
		dirPath := "/users/alice/" + sub
		must(t, client.Call(ctx, "fs/stat", map[string]string{"path": dirPath}, &stat))
		if !stat.IsDir {
			t.Fatalf("expected %s to be a directory", dirPath)
		}
	}

	// 3. Switch to Alice
	var switchResp struct {
		ID string `json:"id"`
	}
	must(t, client.Call(ctx, "profile/switch", map[string]string{"id": "alice"}, &switchResp))
	if switchResp.ID != "alice" {
		t.Fatalf("expected switched id alice, got %s", switchResp.ID)
	}

	// Verify active profile is alice
	must(t, client.Call(ctx, "profile/active", nil, &activeResp))
	if activeResp.ID != "alice" {
		t.Fatalf("expected active profile alice, got %s", activeResp.ID)
	}

	// Verify sys/status reports user: alice
	must(t, client.Call(ctx, "sys/status", nil, &statusResp))
	if statusResp.User != "alice" {
		t.Fatalf("expected sys/status user alice, got %s", statusResp.User)
	}

	// 4. Session attachment belongs to Alice
	var att struct {
		SessionID string `json:"session_id"`
		User      struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
	}
	must(t, client.Call(ctx, "session/attach", map[string]any{"client_type": "shell"}, &att))
	if att.User.Name != "alice" {
		t.Fatalf("expected session user alice, got %+v", att.User)
	}

	// 5. Document isolation: write document in Alice's documents
	docContent := "Alice Confidential Architecture Notes"
	must(t, client.Call(ctx, "fs/write", map[string]string{
		"path":        "/users/alice/documents/notes.txt",
		"data_base64": base64.StdEncoding.EncodeToString([]byte(docContent)),
	}, nil))

	// Document search finds notes.txt
	var searchResp sdk.DocumentSearchResponse
	must(t, client.Call(ctx, "doc/search", map[string]string{"query": "notes"}, &searchResp))
	foundAliceDoc := false
	for _, m := range searchResp.Results {
		if m.Path == "/users/alice/documents/notes.txt" {
			foundAliceDoc = true
			break
		}
	}
	if !foundAliceDoc {
		t.Fatalf("expected notes.txt in search matches, got: %+v", searchResp.Results)
	}

	// 6. Launch an app under Alice and verify process user attribution
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": "com.gostalgia.notes"}, nil))
	var procs []process.Info
	must(t, client.Call(ctx, "proc/list", nil, &procs))
	var notesProc *process.Info
	for i := range procs {
		if procs[i].Name == "com.gostalgia.notes" {
			notesProc = &procs[i]
			break
		}
	}
	if notesProc == nil {
		t.Fatalf("launched notes app not found in process list: %+v", procs)
	}
	if notesProc.User != "alice" {
		t.Fatalf("expected process user alice, got %s", notesProc.User)
	}

	// Clean up process
	must(t, client.Call(ctx, "app/stop", map[string]string{"id": "com.gostalgia.echo"}, nil))

	// 7. Switch back to guest
	must(t, client.Call(ctx, "profile/switch", map[string]string{"id": "guest"}, &switchResp))
	if switchResp.ID != "guest" {
		t.Fatalf("expected switch back to guest, got %s", switchResp.ID)
	}

	must(t, client.Call(ctx, "profile/active", nil, &activeResp))
	if activeResp.ID != "guest" {
		t.Fatalf("expected active profile guest, got %s", activeResp.ID)
	}
}
