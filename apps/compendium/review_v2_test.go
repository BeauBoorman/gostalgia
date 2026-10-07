package compendium

// Reviewer's round-2 reproduction tests (from the user's external reviewer, run on macOS APFS).
// Reconstructed verbatim from the review transcript. Some identifiers (openHostHarness,
// newHostAdapter, adapter.host.SetSaveHooks, crashRecover, importFile, importNotes, v.bytes,
// MaxVaultBytes, MaxNoteBytes, fileStem, draftsOut, exportOut, searchOut, noteOut) refer to
// existing harness/code in apps/compendium at tip 3f89e44.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func reviewV2Params(params any) (p struct {
	Path string `json:"path"`
	Src  string `json:"src"`
}) {
	raw, _ := json.Marshal(params)
	_ = json.Unmarshal(raw, &p)
	return p
}

func TestReviewV2ResolvedCaseOnlyRenameOnHost(t *testing.T) {
	dir := t.TempDir()
	h := openHostHarness(t, newHostAdapter(t, dir))
	h.must(t, "create", map[string]string{"title": "Alpha", "content": "[[Alpha]]"}, nil)
	h.must(t, "rename", map[string]string{"title": "Alpha", "new_title": "alpha"}, nil)
	h2 := openHostHarness(t, newHostAdapter(t, dir))
	var n noteOut
	h2.must(t, "open", map[string]string{"title": "alpha"}, &n)
	if n.Title != "alpha" || n.Content != "[[alpha]]" {
		t.Fatalf("case-only rename did not persist: %+v", n)
	}
}

func TestReviewV2ResolvedRecoveryDurability(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "A", "content": "committed"}, nil)
	artifact := vaultDir + "/A.md.recover"
	h.fs.put(artifact, []byte("recoverable edit"))
	st := store{call: func(ctx context.Context, method string, params, out any) error {
		if method == "fs/save" && reviewV2Params(params).Path == draftsDir+"/A.json" {
			return errors.New("disk full while saving recovered draft")
		}
		return h.fs.call(ctx, method, params, out)
	}}
	v := newVault(st, h.app.now)
	if err := v.load(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, present := h.fs.get(artifact); !present {
		t.Fatal("recovery artifact lost before draft persisted")
	}
	h2 := openHarness(t, h.fs)
	var drafts draftsOut
	h2.must(t, "drafts", nil, &drafts)
	if len(drafts.Drafts) != 1 || drafts.Drafts[0].Content != "recoverable edit" {
		t.Fatalf("retained artifact could not be recovered: %+v", drafts)
	}
}

func TestReviewV2RestoreSalvagePreservesSource(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "A", "content": "v1"}, nil)
	h.must(t, "save", map[string]string{"title": "A", "content": "v2"}, nil)
	var tr struct {
		TrashID string `json:"trash_id"`
	}
	h.must(t, "trash", map[string]string{"title": "A"}, &tr)
	source := trashDir + "/" + tr.TrashID + "/A.md"
	st := store{call: func(ctx context.Context, method string, params, out any) error {
		if method == "fs/rename" && reviewV2Params(params).Src == source {
			return errors.New("persistent failure moving note out of trash")
		}
		return h.fs.call(ctx, method, params, out)
	}}
	h.app.v.st = st
	if err := h.call("restore", map[string]string{"trash_id": tr.TrashID}, nil); err == nil {
		t.Fatal("expected restore failure")
	}
	h2 := openHarness(t, h.fs)
	h2.app.v = newVault(st, h.app.now)
	h2.must(t, "status", nil, nil)
	_, inTrash := h.fs.get(source)
	_, inVault := h.fs.get(vaultDir + "/A.md")
	if !inTrash && !inVault {
		t.Fatalf("reopening deleted the note after its restore move failed; surviving files=%v", h.fs.paths(testRoot))
	}
}

func TestReviewV2StaleRecoveryArtifactPreservesNewerDraft(t *testing.T) {
	dir := t.TempDir()
	adapter := newHostAdapter(t, dir)
	h := openHostHarness(t, adapter)
	h.must(t, "create", map[string]string{"title": "A", "content": "saved"}, nil)
	h.must(t, "edit", map[string]string{"title": "A", "content": "draft v1"}, nil)
	adapter.host.SetSaveHooks(nil, func(string, string) error {
		return errors.New("ordinary draft atomic-rename failure, process remains alive")
	})
	if err := h.call("edit", map[string]string{"title": "A", "content": "draft v2"}, nil); err == nil {
		t.Fatal("expected failed draft replacement")
	}
	adapter.host.SetSaveHooks(nil, nil)
	h.must(t, "edit", map[string]string{"title": "A", "content": "draft v3"}, nil)
	h2 := openHostHarness(t, newHostAdapter(t, dir))
	var d draftsOut
	h2.must(t, "drafts", nil, &d)
	for _, draft := range d.Drafts {
		if draft.Content == "draft v3" {
			return
		}
	}
	t.Fatalf("recovery overwrote the newer successfully staged draft: %+v", d.Drafts)
}

func TestReviewV2DiscardDraftAfterPendingTrash(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "A", "content": "saved"}, nil)
	h.must(t, "edit", map[string]string{"title": "A", "content": "draft"}, nil)
	fail := true
	h.app.v.st.call = func(ctx context.Context, method string, params, out any) error {
		if fail && method == "fs/remove" && reviewV2Params(params).Path == journalPath {
			fail = false
			return errors.New("transient failure removing completed trash journal")
		}
		return h.fs.call(ctx, method, params, out)
	}
	if err := h.call("trash", map[string]string{"title": "A"}, nil); err == nil {
		t.Fatal("expected journal removal failure")
	}
	defer func() {
		if panicValue := recover(); panicValue != nil {
			t.Fatalf("discarding a stale draft panicked after pending trash completed: %v", panicValue)
		}
	}()
	_ = h.call("recover", map[string]any{"title": "A", "restore": false}, nil)
}

func TestReviewV2RenameRespectsNoteSize(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "A"}, nil)
	body := strings.Repeat("x", MaxNoteBytes-len("[[A]]")) + "[[A]]"
	h.must(t, "create", map[string]string{"title": "Large", "content": body}, nil)
	err := h.call("rename", map[string]string{"title": "A", "new_title": "LongTitle"}, nil)
	t.Logf("rename result: %v", err)
	h2 := openHarness(t, h.fs)
	if err := h2.call("open", map[string]string{"title": "Large"}, nil); err != nil {
		t.Fatalf("a successful rename made a formerly valid note inaccessible on restart: %v", err)
	}
}

func TestReviewV2NormalizationDraftCollision(t *testing.T) {
	dir := t.TempDir()
	h := openHostHarness(t, newHostAdapter(t, dir))
	first, second := "Caf\u00e9", "Cafe\u0301"
	h.must(t, "edit", map[string]string{"title": first, "content": "first draft"}, nil)
	alias := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(draftsDir+"/"+fileStem(second)+".json", "/")))
	if _, err := os.Stat(alias); err != nil {
		t.Skip("host filesystem distinguishes Unicode normalization forms")
	}
	t.Logf("second edit result: %v", h.call("edit", map[string]string{"title": second, "content": "second draft"}, nil))
	h2 := openHostHarness(t, newHostAdapter(t, dir))
	var drafts draftsOut
	h2.must(t, "drafts", nil, &drafts)
	for _, d := range drafts.Drafts {
		if d.Content == "first draft" {
			return
		}
	}
	t.Fatalf("normalization-equivalent title silently destroyed the first draft: %+v", drafts.Drafts)
}

func TestReviewV2UnicodeLinkRewrite(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "B"}, nil)
	h.must(t, "create", map[string]string{"title": "A", "content": "[[\u00a0B]]"}, nil)
	h.must(t, "rename", map[string]string{"title": "B", "new_title": "Beta"}, nil)
	stored, _ := h.fs.get(vaultDir + "/A.md")
	if !utf8.Valid(stored) || string(stored) != "[[\u00a0Beta]]" {
		t.Fatalf("rename corrupted a resolved wikilink: bytes=%q valid_UTF8=%v", stored, utf8.Valid(stored))
	}
}

func TestReviewV2ExportImportRoundTrip(t *testing.T) {
	h := openHarness(t, newMockFS())
	body := "# List\n" + strings.Repeat("- item\n", 20000)
	h.must(t, "create", map[string]string{"title": "List", "content": body}, nil)
	var export exportOut
	h.must(t, "export", nil, &export)
	fresh := openHarness(t, newMockFS())
	if err := fresh.call("import", map[string]string{"zip_base64": export.DataBase64}, nil); err != nil {
		t.Fatalf("the app refused its own valid one-note vault export: %v", err)
	}
}

func TestReviewV2PrefixConjunction(t *testing.T) {
	h := openHarness(t, newMockFS())
	for i := 0; i < 65; i++ {
		body := fmt.Sprintf("project%02d", i)
		if i == 64 {
			body += " unique"
		}
		h.must(t, "create", map[string]string{"title": fmt.Sprintf("N%02d", i), "content": body}, nil)
	}
	var out searchOut
	h.must(t, "search", map[string]string{"query": "project unique"}, &out)
	if len(out.Results) != 1 || out.Results[0].Title != "N64" {
		t.Fatalf("ordinary prefix conjunction still misses an existing note: %+v", out)
	}
}

func TestReviewV2BoundSearchSnippets(t *testing.T) {
	h := openHarness(t, newMockFS())
	body := "ab" + strings.Repeat("x", 900<<10)
	for i := 0; i < 5; i++ {
		h.must(t, "create", map[string]string{"title": fmt.Sprintf("Long%d", i), "content": body}, nil)
	}
	var out searchOut
	h.must(t, "search", map[string]string{"query": "ab"}, &out)
	response, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(response) > 4<<20 {
		t.Fatalf("valid search produces a response too large for the socket: %d bytes", len(response))
	}
}

func TestReviewV2ExternalEditConflict(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "A", "content": "initial"}, nil)
	h.fs.put(vaultDir+"/A.md", []byte("external edit"))
	if err := h.call("save", map[string]string{"title": "A", "content": "app edit"}, nil); err != nil {
		return
	}
	for _, p := range h.fs.paths(testRoot) {
		content, _ := h.fs.get(p)
		if string(content) == "external edit" {
			return
		}
	}
	t.Fatal("save destroyed the external edit without a conflict or history copy")
}

func TestReviewV2ImportPreflightAccountsForSkippedNotes(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "Existing", "content": strings.Repeat("e", 1<<20)}, nil)
	// Model the byte counter of a 255 MiB vault without allocating its other
	// 254 MiB: unrelated notes do not otherwise affect this preflight.
	h.app.v.bytes = MaxVaultBytes - (1 << 20)
	files := []importFile{
		{title: "Existing", data: []byte{}},
		{title: "B", data: []byte(strings.Repeat("b", 1<<20))},
		{title: "C", data: []byte(strings.Repeat("c", 1<<20))},
	}
	result, err := h.app.v.importNotes(context.Background(), files, false)
	if err == nil {
		t.Fatal("expected capacity refusal")
	}
	if len(result.Imported) != 0 {
		t.Fatalf("capacity preflight promised no writes but partially imported: %+v, error=%v", result, err)
	}
}
