package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"gostalgia/internal/runtime"
	"gostalgia/sdk"
)

func TestFilesEndToEndLifecycle(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, LogOutput: io.Discard})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("files e2e test") })

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Grant Files app access to /users/guest
	must(t, client.Call(ctx, "fs/grant", map[string]any{
		"app_id":    "com.gostalgia.files",
		"path":      "/users/guest",
		"access":    "read-write",
		"recursive": true,
	}, nil))

	// Grant Notes app access to /users/guest for handoff
	must(t, client.Call(ctx, "fs/grant", map[string]any{
		"app_id":    "com.gostalgia.notes",
		"path":      "/users/guest",
		"access":    "read-write",
		"recursive": true,
	}, nil))

	// Launch Files app
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": "com.gostalgia.files"}, nil))

	const base = "app/com.gostalgia.files/"

	// 1. Initial view verification
	var view sdk.View
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	must(t, view.Validate())
	if !strings.HasPrefix(view.Title, "Files - /users/guest") {
		t.Fatalf("initial title = %q, want 'Files - /users/guest'", view.Title)
	}

	// 2. Create directory and files in VFS
	b64Doc1 := base64.StdEncoding.EncodeToString([]byte("Content of doc 1"))
	must(t, client.Call(ctx, "fs/save", map[string]any{
		"path":        "/users/guest/doc1.txt",
		"data_base64": b64Doc1,
		"overwrite":   true,
	}, nil))

	// Refresh view
	p := sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_refresh",
		Action:    "refresh",
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())

	var doc1ItemID string
	for _, it := range view.Items {
		if strings.Contains(it.Label, "doc1.txt") {
			doc1ItemID = it.ID
			break
		}
	}
	if doc1ItemID == "" {
		t.Fatalf("doc1.txt not found in items: %+v", view.Items)
	}

	// 3. Text preview
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_preview",
		Action:    "preview",
		ItemID:    doc1ItemID,
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if !strings.Contains(view.Status, "Content of doc 1") {
		t.Fatalf("preview status mismatch: %s", view.Status)
	}

	// 4. Create new file via action
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_new_file",
		Action:    "new_file",
		Values:    map[string]string{"target": "doc2.txt"},
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if !strings.Contains(view.Status, "Created file doc2.txt") {
		t.Fatalf("status after new_file = %s", view.Status)
	}

	// Verify file exists via fs/stat
	var statOut struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
	}
	must(t, client.Call(ctx, "fs/stat", map[string]string{"path": "/users/guest/doc2.txt"}, &statOut))
	if statOut.IsDir {
		t.Fatal("created doc2.txt should not be a directory")
	}

	// 5. Create new directory via action
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_new_dir",
		Action:    "new_dir",
		Values:    map[string]string{"target": "subfolder"},
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if !strings.Contains(view.Status, "Created folder subfolder") {
		t.Fatalf("status after new_dir = %s", view.Status)
	}

	// 6. Navigate into subfolder
	var subfolderItemID string
	for _, it := range view.Items {
		if strings.Contains(it.Label, "subfolder") {
			subfolderItemID = it.ID
			break
		}
	}
	if subfolderItemID == "" {
		t.Fatalf("subfolder not found in items: %+v", view.Items)
	}

	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_open_folder",
		Action:    "open",
		ItemID:    subfolderItemID,
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if !strings.HasPrefix(view.Title, "Files - /users/guest/subfolder") {
		t.Fatalf("title after opening folder = %q, want /users/guest/subfolder", view.Title)
	}

	// Navigate up
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_up",
		Action:    "up",
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if !strings.HasPrefix(view.Title, "Files - /users/guest") {
		t.Fatalf("title after up = %q, want /users/guest", view.Title)
	}

	// 7. Copy doc2.txt -> doc2_copy.txt
	var doc2ItemID string
	for _, it := range view.Items {
		if strings.Contains(it.Label, "doc2.txt") {
			doc2ItemID = it.ID
			break
		}
	}
	if doc2ItemID == "" {
		t.Fatal("doc2.txt item not found")
	}

	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_copy",
		Action:    "copy",
		ItemID:    doc2ItemID,
		Values:    map[string]string{"target": "doc2_copy.txt"},
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	must(t, client.Call(ctx, "fs/stat", map[string]string{"path": "/users/guest/doc2_copy.txt"}, &statOut))

	// 8. Rename doc2_copy.txt -> doc2_renamed.txt
	var copyItemID string
	for _, it := range view.Items {
		if strings.Contains(it.Label, "doc2_copy.txt") {
			copyItemID = it.ID
			break
		}
	}
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_rename",
		Action:    "rename",
		ItemID:    copyItemID,
		Values:    map[string]string{"target": "doc2_renamed.txt"},
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	must(t, client.Call(ctx, "fs/stat", map[string]string{"path": "/users/guest/doc2_renamed.txt"}, &statOut))

	// 9. Trash & restore lifecycle
	var renItemID string
	for _, it := range view.Items {
		if strings.Contains(it.Label, "doc2_renamed.txt") {
			renItemID = it.ID
			break
		}
	}

	// Trigger trash prompt
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_trash",
		Action:    "trash",
		ItemID:    renItemID,
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if view.Title != "Files - Confirm Move to Trash" {
		t.Fatalf("expected trash confirmation prompt, got title %q", view.Title)
	}

	// Confirm trash
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_confirm_trash",
		Action:    "confirm_trash",
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if !strings.Contains(view.Status, "Moved doc2_renamed.txt to trash") {
		t.Fatalf("status after trash = %s", view.Status)
	}

	// View trash bin
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_trash_bin",
		Action:    "trash_bin",
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if view.Title != "Files - Trash Bin" {
		t.Fatalf("expected trash bin title, got %q", view.Title)
	}
	if len(view.Items) == 0 || !strings.Contains(view.Items[0].Label, "doc2_renamed.txt") {
		t.Fatalf("expected trash entry in items: %+v", view.Items)
	}

	// Restore from trash
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_restore",
		Action:    "restore",
		ItemID:    view.Items[0].ID,
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if !strings.Contains(view.Status, "Restored") {
		t.Fatalf("status after restore = %s", view.Status)
	}

	// Verify file is restored to VFS
	must(t, client.Call(ctx, "fs/stat", map[string]string{"path": "/users/guest/doc2_renamed.txt"}, &statOut))

	// Return to files view
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_back",
		Action:    "back",
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if !strings.HasPrefix(view.Title, "Files - /users/guest") {
		t.Fatalf("expected /users/guest title, got %q", view.Title)
	}

	// 10. Open in Notes (handoff)
	var finalDocItemID string
	for _, it := range view.Items {
		if strings.Contains(it.Label, "doc1.txt") {
			finalDocItemID = it.ID
			break
		}
	}
	p = sdk.ActionRequest{
		Version:   1,
		Instance:  view.Instance,
		RequestID: "act_open_notes",
		Action:    "open",
		ItemID:    finalDocItemID,
	}
	must(t, client.Call(ctx, base+"action", p, &view))
	must(t, view.Validate())
	if !strings.Contains(view.Status, "Opened doc1.txt in Notes") {
		t.Fatalf("status after open in notes: %s", view.Status)
	}

	// Verify Notes app is active and displaying doc1.txt
	var notesView sdk.View
	must(t, client.Call(ctx, "app/com.gostalgia.notes/view", sdk.ViewRequest{Version: 1}, &notesView))
	must(t, notesView.Validate())
	if notesView.Title != "Notes - doc1.txt" {
		t.Fatalf("notes title after handoff = %q, want 'Notes - doc1.txt'", notesView.Title)
	}

	// 11. Programmatic IPC routes
	var browseOut struct {
		CWD     string `json:"cwd"`
		Entries []any  `json:"entries"`
	}
	must(t, client.Call(ctx, base+"browse", map[string]string{"path": "/users/guest"}, &browseOut))
	if browseOut.CWD != "/users/guest" {
		t.Fatalf("browse cwd = %s", browseOut.CWD)
	}

	var previewOut struct {
		Path    string `json:"path"`
		Preview string `json:"preview"`
	}
	must(t, client.Call(ctx, base+"preview", map[string]any{"path": "/users/guest/doc1.txt"}, &previewOut))
	if !strings.Contains(previewOut.Preview, "Content of doc 1") {
		t.Fatalf("programmatic preview mismatch: %+v", previewOut)
	}

	// 12. Clean stop
	must(t, client.Call(ctx, "app/stop", map[string]string{"id": "com.gostalgia.files"}, nil))
	must(t, client.Call(ctx, "app/stop", map[string]string{"id": "com.gostalgia.notes"}, nil))
}

func TestFilesLargeDirectoryHandling(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, LogOutput: io.Discard})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("files large dir e2e test") })

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	must(t, client.Call(ctx, "fs/grant", map[string]any{
		"app_id":    "com.gostalgia.files",
		"path":      "/users/guest",
		"access":    "read-write",
		"recursive": true,
	}, nil))

	// Create 110 files in VFS
	b64 := base64.StdEncoding.EncodeToString([]byte("hello"))
	for i := 0; i < 110; i++ {
		must(t, client.Call(ctx, "fs/save", map[string]any{
			"path":        fmt.Sprintf("/users/guest/file_%03d.txt", i),
			"data_base64": b64,
			"overwrite":   true,
		}, nil))
	}

	must(t, client.Call(ctx, "app/launch", map[string]string{"id": "com.gostalgia.files"}, nil))
	const base = "app/com.gostalgia.files/"

	var view sdk.View
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	must(t, view.Validate())

	// Ensure view item count never exceeds 100
	if len(view.Items) > 100 {
		t.Fatalf("items count = %d exceeds maximum 100", len(view.Items))
	}

	// Verify truncation indicator
	var hasTruncated bool
	for _, it := range view.Items {
		if it.ID == "item_more" {
			hasTruncated = true
			break
		}
	}
	if !hasTruncated {
		t.Fatal("expected item_more truncation indicator in large directory")
	}

	must(t, client.Call(ctx, "app/stop", map[string]string{"id": "com.gostalgia.files"}, nil))
}
