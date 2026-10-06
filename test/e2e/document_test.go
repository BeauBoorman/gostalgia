package e2e

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"gostalgia/internal/runtime"
	"gostalgia/sdk"
)

func TestDocumentSearchAndLookupE2E(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	defer rt.Shutdown("test complete")

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Create sample documents
	f1 := "/users/guest/documents/alpha.txt"
	f2 := "/users/guest/documents/beta.md"
	f3 := "/users/guest/documents/gamma.txt"
	for _, p := range []string{f1, f2, f3} {
		must(t, client.Call(ctx, "fs/write", map[string]string{
			"path":        p,
			"data_base64": base64.StdEncoding.EncodeToString([]byte("content of " + p)),
		}, nil))
	}

	// 2. Search by extension
	var sResp sdk.DocumentSearchResponse
	must(t, client.Call(ctx, "doc/search", sdk.DocumentSearchQuery{
		Path:      "/users/guest/documents",
		Extension: ".txt",
	}, &sResp))
	if sResp.Total != 2 {
		t.Fatalf("expected 2 .txt files, got %d: %+v", sResp.Total, sResp.Results)
	}

	// 3. Search by query string
	must(t, client.Call(ctx, "doc/search", sdk.DocumentSearchQuery{
		Path:  "/users/guest/documents",
		Query: "beta",
	}, &sResp))
	if sResp.Total != 1 || sResp.Results[0].Path != f2 {
		t.Fatalf("expected beta.md, got %+v", sResp)
	}

	// 4. Record to recents and test indexed lookup
	must(t, client.Call(ctx, "doc/recents/add", map[string]string{"path": f1}, nil))
	var lResp sdk.DocumentSearchResponse
	must(t, client.Call(ctx, "doc/lookup", map[string]any{"query": "alpha"}, &lResp))
	if lResp.Total != 1 || lResp.Results[0].Path != f1 {
		t.Fatalf("expected alpha.txt in lookup, got %+v", lResp)
	}

	// 5. Stale path pruning: remove file and verify lookup prunes it
	must(t, client.Call(ctx, "fs/remove", map[string]string{"path": f1}, nil))
	must(t, client.Call(ctx, "doc/lookup", map[string]any{"query": "alpha"}, &lResp))
	if lResp.Total != 0 {
		t.Fatalf("expected stale missing file to be excluded and pruned from lookup, got %+v", lResp)
	}
}

func TestDocumentRecentsAndFavoritesPersistenceE2E(t *testing.T) {
	root := t.TempDir()

	docA := "/users/guest/documents/docA.txt"
	docB := "/users/guest/documents/docB.txt"

	// --- Phase 1: Boot, save files, add recents and favorites ---
	rt1, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)

	client1 := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	must(t, client1.Call(ctx, "fs/write", map[string]string{
		"path":        docA,
		"data_base64": base64.StdEncoding.EncodeToString([]byte("Doc A content")),
	}, nil))
	must(t, client1.Call(ctx, "fs/write", map[string]string{
		"path":        docB,
		"data_base64": base64.StdEncoding.EncodeToString([]byte("Doc B content")),
	}, nil))

	must(t, client1.Call(ctx, "doc/recents/add", map[string]string{"path": docA}, nil))
	must(t, client1.Call(ctx, "doc/favorites/add", map[string]string{"path": docB, "label": "Favorite B"}, nil))

	client1.Close()
	rt1.Shutdown("restart test")
	rt1.Wait()

	// --- Phase 2: Reboot from the same root and verify persistence ---
	rt2, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	defer rt2.Shutdown("test complete")

	client2 := dialRunning(t, root)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	var recentsOut struct {
		Entries []sdk.RecentDocument `json:"entries"`
		Total   int                  `json:"total"`
	}
	must(t, client2.Call(ctx2, "doc/recents", map[string]bool{"verify_exists": true}, &recentsOut))
	if recentsOut.Total != 1 || recentsOut.Entries[0].Path != docA || !recentsOut.Entries[0].Exists {
		t.Fatalf("recents not restored after reboot: %+v", recentsOut)
	}

	var favsOut struct {
		Entries []sdk.FavoriteDocument `json:"entries"`
		Total   int                    `json:"total"`
	}
	must(t, client2.Call(ctx2, "doc/favorites", map[string]bool{"verify_exists": true}, &favsOut))
	if favsOut.Total != 1 || favsOut.Entries[0].Path != docB || favsOut.Entries[0].Label != "Favorite B" {
		t.Fatalf("favorites not restored after reboot: %+v", favsOut)
	}
}

func TestDocumentHandoffAndGrantIsolationE2E(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	defer rt.Shutdown("test complete")

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create test files
	targetDoc := "/users/guest/documents/secret_project.txt"
	siblingDoc := "/users/guest/documents/sibling_confidential.txt"
	must(t, client.Call(ctx, "fs/write", map[string]string{
		"path":        targetDoc,
		"data_base64": base64.StdEncoding.EncodeToString([]byte("secret plan")),
	}, nil))
	must(t, client.Call(ctx, "fs/write", map[string]string{
		"path":        siblingDoc,
		"data_base64": base64.StdEncoding.EncodeToString([]byte("sibling plan")),
	}, nil))

	// 1. Perform handoff to Notes via doc/handoff
	var hResult sdk.HandoffResult
	must(t, client.Call(ctx, "doc/handoff", sdk.HandoffRequest{
		Version: sdk.DocumentHandoffVersion,
		Path:    targetDoc,
		AppID:   "com.gostalgia.notes",
		Mode:    "read-write",
	}, &hResult))

	if !hResult.Success || hResult.AppID != "com.gostalgia.notes" || hResult.GrantID == "" {
		t.Fatalf("unexpected handoff result: %+v", hResult)
	}

	// 2. Verify Notes is now running and opened targetDoc
	var notesView sdk.View
	must(t, client.Call(ctx, "app/com.gostalgia.notes/view", sdk.ViewRequest{Version: 1}, &notesView))
	if !strings.Contains(notesView.Title, "secret_project.txt") {
		t.Fatalf("expected notes view for secret_project.txt, got %q", notesView.Title)
	}

	// 3. Inspect grant store via fs/grant/list:
	// Verify Notes app received a grant ONLY for targetDoc, NOT for /users/guest/documents or siblingDoc
	var grantsOut struct {
		Grants []struct {
			ID        string `json:"id"`
			AppID     string `json:"app_id"`
			Path      string `json:"path"`
			Access    string `json:"access"`
			Recursive bool   `json:"recursive"`
			Revoked   bool   `json:"revoked"`
		} `json:"grants"`
	}
	must(t, client.Call(ctx, "fs/grant/list", map[string]string{"app_id": "com.gostalgia.notes"}, &grantsOut))

	var targetGrantFound bool
	for _, g := range grantsOut.Grants {
		if g.AppID == "com.gostalgia.notes" && !g.Revoked {
			if g.Path == targetDoc {
				targetGrantFound = true
				if g.Recursive {
					t.Fatalf("SECURITY VIOLATION: grant for targetDoc must not be recursive")
				}
			} else {
				t.Fatalf("SECURITY VIOLATION: Notes was granted unexpected path: %+v", g)
			}
		}
	}
	if !targetGrantFound {
		t.Fatalf("expected grant for targetDoc not found in grants list: %+v", grantsOut.Grants)
	}

	// 4. Verify revocation: revoke the grant on targetDoc and ensure Notes can no longer save it
	must(t, client.Call(ctx, "fs/grant/revoke", map[string]string{"id": hResult.GrantID}, nil))

	var saveResp struct {
		Error string `json:"error"`
	}
	err = client.Call(ctx, "app/com.gostalgia.notes/save", map[string]any{
		"path": targetDoc,
	}, &saveResp)
	// Because Notes calls fs/save which checks grants, save should now fail
	if err == nil {
		t.Fatalf("expected save to fail after grant revocation")
	}

	// 5. Stale / missing file rejection:
	err = client.Call(ctx, "doc/handoff", sdk.HandoffRequest{
		Version: sdk.DocumentHandoffVersion,
		Path:    "/users/guest/documents/missing_file.txt",
		AppID:   "com.gostalgia.notes",
	}, nil)
	if err == nil {
		t.Fatalf("expected error for non-existent file")
	}
}
