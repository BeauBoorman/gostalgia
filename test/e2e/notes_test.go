package e2e

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"time"

	"gostalgia/internal/runtime"
	"gostalgia/sdk"
)

func TestNotesEndToEndLifecycleAndRecovery(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, LogOutput: io.Discard})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("notes e2e test") })

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Grant Notes access to /users/guest/documents
	must(t, client.Call(ctx, "fs/grant", map[string]any{
		"app_id":    "com.gostalgia.notes",
		"path":      "/users/guest/documents",
		"access":    "read-write",
		"recursive": true,
	}, nil))

	// Launch Notes
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": "com.gostalgia.notes"}, nil))

	const base = "app/com.gostalgia.notes/"

	// 1. Initial view verification
	var view sdk.View
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	must(t, view.Validate())
	if view.Title != "Notes - [Untitled]" {
		t.Fatalf("initial title = %q, want 'Notes - [Untitled]'", view.Title)
	}

	pathFieldID := view.Fields[0].ID
	contentFieldID := view.Fields[1].ID

	docPath := "/users/guest/documents/notes_test.txt"
	testContent := "First line of notes.\nSecond line: Unicode 日本語 🚀"

	// 2. Save new document via presentation action
	p := sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act1",
		Action:    "save",
		Values: map[string]string{
			pathFieldID:    docPath,
			contentFieldID: testContent,
		},
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if view.Title != "Notes - notes_test.txt" {
		t.Fatalf("saved title = %q, want 'Notes - notes_test.txt'", view.Title)
	}
	if strings.Contains(view.Title, "*") {
		t.Fatalf("saved title should not be dirty: %s", view.Title)
	}

	// Verify the file was written to VFS via fs/read
	var readOut struct {
		DataBase64 string `json:"data_base64"`
	}
	must(t, client.Call(ctx, "fs/read", map[string]string{"path": docPath}, &readOut))
	rawContent, err := base64.StdEncoding.DecodeString(readOut.DataBase64)
	must(t, err)
	if string(rawContent) != testContent {
		t.Fatalf("VFS file content = %q, want %q", string(rawContent), testContent)
	}

	// 3. Make an unsaved edit
	unsavedContent := testContent + "\nUnsaved draft line that would be lost on crash."
	must(t, client.Call(ctx, base+"edit", map[string]string{"content": unsavedContent}, nil))

	// Refresh view: should show dirty marker '*'
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	must(t, view.Validate())
	if !strings.HasSuffix(view.Title, "*") {
		t.Fatalf("dirty title = %q, want trailing '*'", view.Title)
	}

	// Check that recovery draft exists in app-private storage
	var recRead struct {
		DataBase64 string `json:"data_base64"`
	}
	must(t, client.Call(ctx, "fs/read", map[string]string{"path": "/apps/data/com.gostalgia.notes/recovery.json"}, &recRead))
	recJSON, err := base64.StdEncoding.DecodeString(recRead.DataBase64)
	must(t, err)
	if !strings.Contains(string(recJSON), "Unsaved draft line that would be lost on crash") {
		t.Fatalf("recovery draft payload missing unsaved content: %s", string(recJSON))
	}

	// 4. Stop Notes while dirty (simulating app exit / crash with unsaved work)
	must(t, client.Call(ctx, "app/stop", map[string]string{"id": "com.gostalgia.notes"}, nil))

	// 5. Relaunch Notes: it should detect the recovery draft and enter PromptRecovery
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": "com.gostalgia.notes"}, nil))

	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	must(t, view.Validate())
	if view.Title != "Notes - Crash Recovery Draft Found" {
		t.Fatalf("title on relaunch with recovery = %q, want 'Notes - Crash Recovery Draft Found'", view.Title)
	}

	// 6. Restore the recovery draft
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_restore",
		Action:    "restore",
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if view.Title != "Notes - notes_test.txt*" {
		t.Fatalf("restored title = %q, want 'Notes - notes_test.txt*'", view.Title)
	}

	// 7. Save the restored draft
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_save_restored",
		Action:    "save",
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if view.Title != "Notes - notes_test.txt" {
		t.Fatalf("title after saving restored draft = %q, want 'Notes - notes_test.txt'", view.Title)
	}

	// Verify VFS now contains the restored & committed content
	must(t, client.Call(ctx, "fs/read", map[string]string{"path": docPath}, &readOut))
	finalContent, err := base64.StdEncoding.DecodeString(readOut.DataBase64)
	must(t, err)
	if string(finalContent) != unsavedContent {
		t.Fatalf("committed content = %q, want %q", string(finalContent), unsavedContent)
	}

	// Verify recovery file is cleaned up after save
	if err := client.Call(ctx, "fs/read", map[string]string{"path": "/apps/data/com.gostalgia.notes/recovery.json"}, &readOut); err == nil {
		t.Fatal("expected recovery draft to be removed after save")
	}

	// 8. Clean stop
	must(t, client.Call(ctx, "app/stop", map[string]string{"id": "com.gostalgia.notes"}, nil))
}

func TestNotesExternalModificationSaveConflict(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, LogOutput: io.Discard})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("notes conflict test") })

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Grant Notes access to /users/guest/documents
	must(t, client.Call(ctx, "fs/grant", map[string]any{
		"app_id":    "com.gostalgia.notes",
		"path":      "/users/guest/documents",
		"access":    "read-write",
		"recursive": true,
	}, nil))

	docPath := "/users/guest/documents/shared_note.txt"
	initialData := base64.StdEncoding.EncodeToString([]byte("Initial content"))
	must(t, client.Call(ctx, "fs/save", map[string]any{"path": docPath, "data_base64": initialData, "overwrite": true}, nil))

	// Launch Notes and open the note
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": "com.gostalgia.notes"}, nil))
	const base = "app/com.gostalgia.notes/"

	must(t, client.Call(ctx, base+"open", map[string]any{"path": docPath}, nil))

	// External process modifies docPath directly via fs/save with at least 1 second difference
	time.Sleep(1100 * time.Millisecond)
	externalData := base64.StdEncoding.EncodeToString([]byte("Overwritten externally!"))
	must(t, client.Call(ctx, "fs/save", map[string]any{"path": docPath, "data_base64": externalData, "overwrite": true}, nil))

	// Notes edits its local copy
	must(t, client.Call(ctx, base+"edit", map[string]string{"content": "Notes local edit"}, nil))

	// Notes attempts to save without force
	var saveErrOut any
	err = client.Call(ctx, base+"save", map[string]any{"path": docPath}, &saveErrOut)
	if err == nil {
		t.Fatal("expected save to fail with conflict error on external modification")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected conflict error, got: %v", err)
	}

	// Inspect presentation view: should be in PromptConflict
	var view sdk.View
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	must(t, view.Validate())
	if !strings.HasPrefix(view.Title, "Notes - Save Conflict") {
		t.Fatalf("expected Save Conflict title, got %q", view.Title)
	}

	// Resolve conflict by force_save action
	p := sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_force",
		Action:    "force_save",
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if view.Title != "Notes - shared_note.txt" {
		t.Fatalf("title after force_save = %q, want 'Notes - shared_note.txt'", view.Title)
	}

	// Verify local edits are now saved on disk
	var readOut struct {
		DataBase64 string `json:"data_base64"`
	}
	must(t, client.Call(ctx, "fs/read", map[string]string{"path": docPath}, &readOut))
	diskBytes, err := base64.StdEncoding.DecodeString(readOut.DataBase64)
	must(t, err)
	if string(diskBytes) != "Notes local edit" {
		t.Fatalf("final disk content = %q, want 'Notes local edit'", string(diskBytes))
	}

	must(t, client.Call(ctx, "app/stop", map[string]string{"id": "com.gostalgia.notes"}, nil))
}
