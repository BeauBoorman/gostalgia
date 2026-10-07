package e2e

import (
	"archive/zip"
	"bytes"
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

// TestCompendiumEndToEnd drives Compendium through the real runtime over a
// real socket: an ipc-only manifest, storage confined to its private
// partition, presentation data validated, user-visible paths reachable only
// through operator grants, and state that survives a relaunch.
func TestCompendiumEndToEnd(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, LogOutput: io.Discard})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("compendium e2e test") })
	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const id = "com.gostalgia.compendium"
	const base = "app/" + id + "/"
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": id}, nil))

	var view sdk.View
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	must(t, view.Validate())
	if view.Title != "Compendium" {
		t.Fatalf("initial title = %q", view.Title)
	}

	must(t, client.Call(ctx, base+"create", map[string]string{"title": "Atlas", "content": "Index of [[Harbor]] and [[Lighthouse]]. #map\n"}, nil))
	must(t, client.Call(ctx, base+"create", map[string]string{"title": "Harbor", "content": "Quiet water, a lantern on the pier. #place\n"}, nil))
	var harbor struct {
		Backlinks []struct {
			Title string `json:"title"`
		} `json:"backlinks"`
	}
	must(t, client.Call(ctx, base+"open", map[string]string{"title": "Harbor"}, &harbor))
	if len(harbor.Backlinks) != 1 || harbor.Backlinks[0].Title != "Atlas" {
		t.Fatalf("Harbor backlinks = %+v", harbor.Backlinks)
	}

	// Storage is plain Markdown inside the app-private partition.
	var raw struct {
		DataBase64 string `json:"data_base64"`
	}
	must(t, client.Call(ctx, "fs/read", map[string]string{"path": "/apps/data/" + id + "/vault/Harbor.md"}, &raw))
	if b, _ := base64.StdEncoding.DecodeString(raw.DataBase64); string(b) != "Quiet water, a lantern on the pier. #place\n" {
		t.Fatalf("vault file = %q", b)
	}

	var search struct {
		Results []struct {
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	must(t, client.Call(ctx, base+"search", map[string]string{"query": "lantern"}, &search))
	if len(search.Results) != 1 || search.Results[0].Title != "Harbor" || !strings.Contains(search.Results[0].Snippet, "«lantern»") {
		t.Fatalf("search = %+v", search.Results)
	}

	var graph struct {
		Unresolved []struct {
			Target string `json:"target"`
		} `json:"unresolved"`
	}
	must(t, client.Call(ctx, base+"graph", nil, &graph))
	if len(graph.Unresolved) != 1 || graph.Unresolved[0].Target != "Lighthouse" {
		t.Fatalf("graph unresolved = %+v", graph.Unresolved)
	}

	// Presentation action over IPC: run a search from the command field.
	must(t, client.Call(ctx, base+"view", sdk.ViewRequest{Version: 1}, &view))
	var q string
	for _, f := range view.Fields {
		q = f.ID
	}
	must(t, client.Call(ctx, base+"action", sdk.ActionRequest{Version: 1, Instance: view.Instance, RequestID: "c1",
		Action: "run", Values: map[string]string{q: "graph"}}, &view))
	must(t, view.Validate())
	if view.Title != "Compendium — Graph" {
		t.Fatalf("graph view title = %q", view.Title)
	}

	// No grant: user-visible paths are out of reach.
	doc := []byte("# Imported\r\nfrom the documents folder [[Atlas]]\r\n")
	must(t, client.Call(ctx, "fs/save", map[string]any{"path": "/users/guest/documents/Imported.md",
		"data_base64": base64.StdEncoding.EncodeToString(doc), "overwrite": true}, nil))
	if err := client.Call(ctx, base+"import", map[string]string{"path": "/users/guest/documents/Imported.md"}, nil); err == nil {
		t.Fatal("import from an ungranted user path succeeded")
	}
	if err := client.Call(ctx, base+"export", map[string]string{"title": "Atlas", "dest": "/users/guest/documents/Atlas.md"}, nil); err == nil {
		t.Fatal("export to an ungranted user path succeeded")
	}
	// With an operator grant for exactly those files, both work, byte for byte.
	must(t, client.Call(ctx, "fs/grant", map[string]any{"app_id": id, "path": "/users/guest/documents/Imported.md", "access": "read"}, nil))
	must(t, client.Call(ctx, "fs/grant", map[string]any{"app_id": id, "path": "/users/guest/documents/Atlas.md", "access": "read-write"}, nil))
	must(t, client.Call(ctx, base+"import", map[string]string{"path": "/users/guest/documents/Imported.md"}, nil))
	var note struct {
		Content string `json:"content"`
	}
	must(t, client.Call(ctx, base+"open", map[string]string{"title": "Imported"}, &note))
	if note.Content != string(doc) {
		t.Fatalf("imported content = %q", note.Content)
	}
	must(t, client.Call(ctx, base+"export", map[string]string{"title": "Atlas", "dest": "/users/guest/documents/Atlas.md"}, nil))
	must(t, client.Call(ctx, "fs/read", map[string]string{"path": "/users/guest/documents/Atlas.md"}, &raw))
	if b, _ := base64.StdEncoding.DecodeString(raw.DataBase64); string(b) != "Index of [[Harbor]] and [[Lighthouse]]. #map\n" {
		t.Fatalf("exported file = %q", b)
	}

	// Trash and restore, then a vault export.
	var tr struct {
		TrashID string `json:"trash_id"`
	}
	must(t, client.Call(ctx, base+"trash", map[string]string{"title": "Harbor"}, &tr))
	must(t, client.Call(ctx, base+"restore", map[string]string{"trash_id": tr.TrashID}, nil))
	var ex struct {
		DataBase64 string `json:"data_base64"`
		Files      int    `json:"files"`
	}
	must(t, client.Call(ctx, base+"export", map[string]string{}, &ex))
	zipData, _ := base64.StdEncoding.DecodeString(ex.DataBase64)
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	must(t, err)
	if ex.Files != 3 || len(zr.File) != 3 {
		t.Fatalf("vault export files = %d/%d", ex.Files, len(zr.File))
	}

	// Relaunch: everything is durable and the index needs no rebuild.
	must(t, client.Call(ctx, "app/stop", map[string]string{"id": id}, nil))
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": id}, nil))
	var st struct {
		Notes int `json:"notes"`
		Index struct {
			Rebuilt bool `json:"rebuilt"`
		} `json:"index"`
	}
	must(t, client.Call(ctx, base+"status", nil, &st))
	if st.Notes != 3 || st.Index.Rebuilt {
		t.Fatalf("status after relaunch = %+v", st)
	}
	must(t, client.Call(ctx, base+"open", map[string]string{"title": "Harbor"}, &harbor))
	if len(harbor.Backlinks) != 1 {
		t.Fatalf("backlinks after relaunch = %+v", harbor.Backlinks)
	}
}

func TestCompendiumLargeMetadataOverSocket(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, LogOutput: io.Discard})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("compendium metadata test") })
	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const id = "com.gostalgia.compendium"
	const base = "app/" + id + "/"
	var body strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&body, "[[%s%05d]] ", strings.Repeat("&", 180), i)
	}
	must(t, client.Call(ctx, "fs/save", map[string]any{
		"path":        "/apps/data/" + id + "/vault/Links.md",
		"data_base64": base64.StdEncoding.EncodeToString([]byte(body.String())),
		"overwrite":   false,
	}, nil))
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": id}, nil))

	offset := 0
	for {
		var page struct {
			Links []struct {
				Target string `json:"target"`
			} `json:"links"`
			Total int  `json:"links_total"`
			Next  *int `json:"links_next_offset"`
		}
		must(t, client.Call(ctx, base+"open", map[string]any{
			"title": "Links", "include_content": false, "links_offset": offset,
		}, &page))
		if page.Total != 5000 || len(page.Links) == 0 {
			t.Fatalf("metadata page = %+v", page)
		}
		offset += len(page.Links)
		if page.Next == nil {
			if offset != 5000 {
				t.Fatalf("socket pagination returned %d links", offset)
			}
			break
		}
		if *page.Next != offset {
			t.Fatalf("next offset=%d, want %d", *page.Next, offset)
		}
	}
}
