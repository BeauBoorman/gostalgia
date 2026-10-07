package compendium

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPartialRestoreKeepsDraftReachable(t *testing.T) {
	fs := newMockFS()
	h := openHarness(t, fs)
	h.must(t, "create", map[string]string{"title": "A", "content": "v1"}, nil)
	h.must(t, "save", map[string]string{"title": "A", "content": "v2"}, nil)
	h.must(t, "edit", map[string]string{"title": "A", "content": "unsaved edit"}, nil)
	var tr struct {
		TrashID string `json:"trash_id"`
	}
	h.must(t, "trash", map[string]string{"title": "A"}, &tr)
	entry := trashDir + "/" + tr.TrashID
	fs.fail = func(method, p string) error {
		if method == "fs/rename" && p == entry+"/history" {
			return errors.New("history temporarily unavailable")
		}
		return nil
	}
	if err := h.call("restore", map[string]string{"trash_id": tr.TrashID}, nil); err == nil {
		t.Fatal("expected restore failure")
	}
	h2 := openHarness(t, fs)
	var drafts draftsOut
	h2.must(t, "drafts", nil, &drafts)
	if len(drafts.Drafts) != 1 || drafts.Drafts[0].Content != "unsaved edit" {
		t.Fatalf("partial restore hid the draft: %+v", drafts)
	}
	if _, ok := fs.get(journalPath); !ok {
		t.Fatal("partial restore lost its resumable journal")
	}
	fs.fail = nil
	h2.must(t, "recover", map[string]any{"title": "A", "restore": true}, nil)
	if got := openContent(t, h2, "A"); got != "unsaved edit" {
		t.Fatalf("recovered content = %q", got)
	}
	var history historyOut
	h2.must(t, "history", map[string]string{"title": "A"}, &history)
	if len(history.Versions) != 2 {
		t.Fatalf("history after retry = %+v", history)
	}
	if len(fs.paths(entry+"/")) != 0 {
		t.Fatalf("completed restore left files in trash: %v", fs.paths(entry+"/"))
	}
}

func TestDraftRecoveryAfterExternalDeletion(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "A", "content": "initial"}, nil)
	h.fs.del(vaultDir + "/A.md")
	if err := h.call("save", map[string]string{"title": "A", "content": "recover me"}, nil); err == nil {
		t.Fatal("expected initial deletion conflict")
	}
	h.must(t, "recover", map[string]any{"title": "A", "restore": true}, nil)
	if got := openContent(t, h, "A"); got != "recover me" {
		t.Fatalf("recreated content = %q", got)
	}
}

func TestRenameRefreshesExternalBacklinks(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "A", "content": "initial"}, nil)
	h.must(t, "create", map[string]string{"title": "B"}, nil)
	h.fs.put(vaultDir+"/A.md", []byte("external [[B]]"))
	// The first attempt must refresh stale sources without changing titles.
	if err := h.call("rename", map[string]string{"title": "B", "new_title": "Beta"}, nil); err == nil {
		t.Fatal("rename accepted stale backlink candidates")
	}
	h.must(t, "open", map[string]string{"title": "B"}, nil)
	h.must(t, "rename", map[string]string{"title": "B", "new_title": "Beta"}, nil)
	h2 := openHarness(t, h.fs)
	if got := openContent(t, h2, "A"); got != "external [[Beta]]" {
		t.Fatalf("rename missed external backlink: %q", got)
	}
}

func TestImportIdentityNamespacesRoundTrip(t *testing.T) {
	h := openHarness(t, newMockFS())
	for _, title := range []string{"X", "norm:X", "norm:norm:X"} {
		h.must(t, "create", map[string]string{"title": title, "content": title}, nil)
	}
	var archive exportOut
	h.must(t, "export", nil, &archive)
	fresh := openHarness(t, newMockFS())
	var result importResult
	fresh.must(t, "import", map[string]string{"zip_base64": archive.DataBase64}, &result)
	if len(result.Imported) != 3 || len(result.Skipped) != 0 {
		t.Fatalf("distinct import identities collided: %+v", result)
	}
}

func TestImportPreflightsIntermediateCapacity(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "Existing", "content": strings.Repeat("e", 1<<20)}, nil)
	// Unrelated notes affect only this counter; avoid allocating another 254 MiB.
	h.app.v.bytes = MaxVaultBytes - (1 << 20)
	files := []importFile{
		{title: "B", data: []byte(strings.Repeat("b", 1<<20))},
		{title: "C", data: []byte(strings.Repeat("c", 1<<20))},
		{title: "Existing", data: []byte{}},
	}
	result, err := h.app.v.importNotes(context.Background(), files, true)
	if err == nil || len(result.Imported) != 0 {
		t.Fatalf("unsafe ordered import was not refused before writing: %+v, %v", result, err)
	}
	for _, title := range []string{"B", "C"} {
		if _, ok := h.fs.get(vaultDir + "/" + title + ".md"); ok {
			t.Fatalf("capacity refusal wrote %s", title)
		}
	}
}

func TestOpenBoundsAndPaginatesLinkMetadata(t *testing.T) {
	fs := newMockFS()
	var body strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&body, "[[%s%05d]] ", strings.Repeat("&", 180), i)
	}
	fs.put(vaultDir+"/Links.md", []byte(body.String()))
	h := openHarness(t, fs)
	params := map[string]any{"title": "Links"}
	seen := 0
	for {
		var result struct {
			Links []linkOut `json:"links"`
			Next  *int      `json:"links_next_offset"`
			Total int       `json:"links_total"`
		}
		raw, _ := json.Marshal(params)
		reply, err := h.handlers["open"](context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(reply)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) >= 4<<20 {
			t.Fatalf("open response exceeds frame budget: %d bytes", len(encoded))
		}
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		seen += len(result.Links)
		if result.Next == nil {
			if seen != 5000 || result.Total != 5000 {
				t.Fatalf("paged metadata lost links: seen=%d total=%d", seen, result.Total)
			}
			break
		}
		if *result.Next <= seen-len(result.Links) {
			t.Fatal("metadata pagination did not advance")
		}
		params["links_offset"] = *result.Next
	}
}

func TestOpenRejectsInvalidMetadataOffsets(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "A", "content": "[[B]] #tag"}, nil)
	for _, key := range []string{"links_offset", "backlinks_offset", "tags_offset"} {
		for _, offset := range []int{-1, 2} {
			if err := h.call("open", map[string]any{"title": "A", key: offset}, nil); err == nil {
				t.Fatalf("accepted %s=%d", key, offset)
			}
		}
	}
	var page map[string]any
	h.must(t, "open", map[string]any{"title": "A", "include_content": false}, &page)
	if _, ok := page["content"]; ok {
		t.Fatal("metadata-only response included content")
	}
}

func TestOpenRetainsSourceOfOversizedMetadataItem(t *testing.T) {
	fs := newMockFS()
	body := "[[" + strings.Repeat("&", MaxNoteBytes-4) + "]]"
	fs.put(vaultDir+"/A.md", []byte(body))
	h := openHarness(t, fs)
	var result map[string]any
	h.must(t, "open", map[string]string{"title": "A"}, &result)
	if result["links_omitted_items"] != float64(1) {
		t.Fatalf("oversized item was not explicitly omitted: %v", result["links_omitted_items"])
	}
	content, err := base64.StdEncoding.DecodeString(result["content_base64"].(string))
	if err != nil || string(content) != body {
		t.Fatal("omitting metadata lost its exact source bytes")
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) >= 4<<20 {
		t.Fatalf("oversized-item response is not bounded: %d bytes, %v", len(encoded), err)
	}
}
