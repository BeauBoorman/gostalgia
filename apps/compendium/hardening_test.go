package compendium

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"gostalgia/sdk"
)

// Regression tests for the external review's findings. Each reproduces the
// reported failure through the public routes or the presentation, against
// the in-memory VFS model extended with case folding, frame limits, and
// ordinary (non-crash) I/O failure injection.

const (
	testJournal = testRoot + "/journal.json"
	testDrafts  = testRoot + "/drafts"
	testHistory = testRoot + "/history"
)

type warnStatus struct {
	Notes int `json:"notes"`
	Index struct {
		Warnings []string `json:"warnings"`
	} `json:"index"`
}

func openContent(t *testing.T, h *harness, title string) string {
	t.Helper()
	var n noteOut
	h.must(t, "open", map[string]string{"title": title}, &n)
	return n.Content
}

func draftContents(t *testing.T, h *harness) map[string]string {
	t.Helper()
	var d draftsOut
	h.must(t, "drafts", nil, &d)
	out := map[string]string{}
	for _, x := range d.Drafts {
		out[x.Title] = x.Content
	}
	return out
}

func mustReopen(t *testing.T, fs *mockFS) *harness {
	t.Helper()
	h := openHarness(t, fs)
	if err := h.call("status", nil, nil); err != nil {
		t.Fatalf("vault no longer opens: %v", err)
	}
	return h
}

// [P1] A capitalization-only rename on a case-insensitive host must succeed
// and must never leave a journal that stops the vault from opening.
func TestRenameCaseOnlyOnCaseInsensitiveHost(t *testing.T) {
	fs := newMockFS()
	fs.foldCase = true
	h := openHarness(t, fs)
	h.must(t, "create", map[string]string{"title": "note", "content": "v1 [[note]]\n"}, nil)
	h.must(t, "save", map[string]string{"title": "note", "content": "v2 [[note]]\n"}, nil)
	h.must(t, "create", map[string]string{"title": "Linker", "content": "see [[note]]\n"}, nil)
	h.must(t, "edit", map[string]string{"title": "note", "content": "draft [[note]]\n"}, nil)

	if err := h.call("rename", map[string]string{"title": "note", "new_title": "Note"}, nil); err != nil {
		t.Errorf("case-only rename failed on a case-insensitive host: %v", err)
	}
	h2 := mustReopen(t, fs)
	var n noteOut
	h2.must(t, "open", map[string]string{"title": "Note"}, &n)
	if n.Title != "Note" || n.Content != "v2 [[Note]]\n" {
		t.Fatalf("renamed note = %q %q", n.Title, n.Content)
	}
	if got := openContent(t, h2, "Linker"); got != "see [[Note]]\n" {
		t.Fatalf("linker = %q", got)
	}
	if _, ok := fs.get(testVault + "/Note.md"); !ok {
		t.Fatalf("file not renamed to Note.md: %v", fs.paths(testVault))
	}
	if _, ok := fs.get(testJournal); ok {
		t.Fatal("journal left behind")
	}
	var hist historyOut
	h2.must(t, "history", map[string]string{"title": "Note"}, &hist)
	if len(hist.Versions) != 1 {
		t.Fatalf("history did not follow the rename: %+v", hist.Versions)
	}
	if d := draftContents(t, h2); d["Note"] != "draft [[Note]]\n" || len(d) != 1 {
		t.Fatalf("draft did not follow the rename: %+v", d)
	}
}

// [P1] Renaming onto a title held by a pending draft (or any name that
// collides ignoring case) is rejected up front and changes nothing.
func TestRenameCollisionsRejectedUpFront(t *testing.T) {
	for _, foldCase := range []bool{false, true} {
		t.Run(fmt.Sprintf("foldCase=%v", foldCase), func(t *testing.T) {
			fs := newMockFS()
			fs.foldCase = foldCase
			h := openHarness(t, fs)
			h.must(t, "create", map[string]string{"title": "A", "content": "a [[A]]\n"}, nil)
			h.must(t, "edit", map[string]string{"title": "A", "content": "a draft\n"}, nil)
			h.must(t, "edit", map[string]string{"title": "B", "content": "unsaved B\n"}, nil)
			for _, to := range []string{"B", "b"} {
				if err := h.call("rename", map[string]string{"title": "A", "new_title": to}, nil); err == nil {
					t.Errorf("rename A -> %q accepted although a draft titled B exists", to)
				}
			}
			h2 := mustReopen(t, fs)
			if got := openContent(t, h2, "A"); got != "a [[A]]\n" {
				t.Fatalf("A changed: %q", got)
			}
			if d := draftContents(t, h2); d["A"] != "a draft\n" || d["B"] != "unsaved B\n" {
				t.Fatalf("drafts changed: %+v", d)
			}
			if _, ok := fs.get(testJournal); ok {
				t.Fatal("journal left behind")
			}

			// Stale history whose name differs only by case collides on a
			// case-insensitive host: reject, or complete; never strand.
			fs.put(testHistory+"/c/00000000000000000001.md", []byte("stale"))
			err := h2.call("rename", map[string]string{"title": "A", "new_title": "C"}, nil)
			h3 := mustReopen(t, fs)
			if err == nil {
				openContent(t, h3, "C")
			} else {
				openContent(t, h3, "A")
			}
			if _, ok := fs.get(testJournal); ok {
				t.Fatal("journal left behind")
			}
		})
	}
}

// [P1] A journal that cannot be completed must not stop the vault from
// opening: it is quarantined, the problem is reported, and nothing is
// clobbered by the steps that could not safely run.
func TestUnreplayableJournalDoesNotBrickOpen(t *testing.T) {
	fs := newMockFS()
	h := openHarness(t, fs)
	h.must(t, "create", map[string]string{"title": "A", "content": "alpha\n"}, nil)
	h.must(t, "create", map[string]string{"title": "B", "content": "beta\n"}, nil)
	j, _ := json.Marshal(map[string]any{"version": 1, "kind": "rename", "ops": []map[string]any{
		{"op": "move", "src": testVault + "/A.md", "dst": testVault + "/B.md"},
		{"op": "write", "path": testVault + "/B.md", "data": []byte("clobbered")},
	}})
	fs.put(testJournal, j)

	h2 := mustReopen(t, fs)
	if got := openContent(t, h2, "A"); got != "alpha\n" {
		t.Fatalf("A = %q", got)
	}
	if got := openContent(t, h2, "B"); got != "beta\n" {
		t.Fatalf("B = %q (a step after the failed move clobbered it)", got)
	}
	if _, ok := fs.get(testJournal); ok {
		t.Fatal("unreplayable journal still pending")
	}
	var st warnStatus
	h2.must(t, "status", nil, &st)
	if !strings.Contains(strings.Join(st.Index.Warnings, "\n"), "rename") {
		t.Fatalf("problem not reported: %+v", st.Index.Warnings)
	}
	if v := h2.view(t); !strings.Contains(v.Status, "storage notice") {
		t.Fatalf("home view does not surface the problem: %q", v.Status)
	}
	quarantined := false
	for _, p := range fs.paths(testRoot) {
		quarantined = quarantined || strings.Contains(p, "journal") && p != testJournal
	}
	if !quarantined {
		t.Fatalf("journal not kept for inspection: %v", fs.paths(testRoot))
	}
}

// A journal written by the earlier build for a case-only rename replays on
// a case-insensitive host instead of failing forever.
func TestLegacyCaseOnlyJournalReplays(t *testing.T) {
	fs := newMockFS()
	fs.foldCase = true
	h := openHarness(t, fs)
	h.must(t, "create", map[string]string{"title": "note", "content": "body\n"}, nil)
	j, _ := json.Marshal(map[string]any{"version": 1, "kind": "rename", "ops": []map[string]any{
		{"op": "move", "src": testVault + "/note.md", "dst": testVault + "/Note.md"},
	}})
	fs.put(testJournal, j)
	h2 := mustReopen(t, fs)
	var n noteOut
	h2.must(t, "open", map[string]string{"title": "note"}, &n)
	if n.Title != "Note" || n.Content != "body\n" {
		t.Fatalf("legacy journal not replayed: %q %q", n.Title, n.Content)
	}
}

// [P1] A .recover artifact is the only copy of a failed save; it must stay
// on disk until its replacement draft is durably written.
func TestRecoverArtifactKeptUntilDraftIsDurable(t *testing.T) {
	fs := newMockFS()
	h := openHarness(t, fs)
	h.must(t, "create", map[string]string{"title": "A", "content": "old\n"}, nil)
	fs.put(testVault+"/A.md.recover", []byte("newer\n"))
	fs.fail = func(method, p string) error {
		if method == "fs/save" && strings.HasPrefix(p, testDrafts+"/") {
			return errors.New("io: disk full")
		}
		return nil
	}
	_ = openHarness(t, fs).call("status", nil, nil) // may fail: the disk is full
	fs.fail = nil
	h3 := mustReopen(t, fs)
	found := false
	for _, c := range draftContents(t, h3) {
		found = found || c == "newer\n"
	}
	if !found {
		t.Fatalf("salvaged content lost after a failed draft write; drafts=%+v files=%v", draftContents(t, h3), fs.paths(testRoot))
	}
}

func setupPendingRename(t *testing.T) (*mockFS, *harness) {
	fs := newMockFS()
	h := openHarness(t, fs)
	h.must(t, "create", map[string]string{"title": "A", "content": "alpha\n"}, nil)
	h.must(t, "save", map[string]string{"title": "A", "content": "alpha 2\n"}, nil)
	h.must(t, "create", map[string]string{"title": "Linker", "content": "see [[A]]\n"}, nil)
	h.must(t, "create", map[string]string{"title": "C", "content": "c\n"}, nil)
	return fs, h
}

func checkRenamed(t *testing.T, h *harness) {
	t.Helper()
	if got := openContent(t, h, "B"); got != "alpha 2\n" {
		t.Fatalf("B = %q", got)
	}
	if got := openContent(t, h, "Linker"); got != "see [[B]]\n" {
		t.Fatalf("Linker = %q: the rename was stranded", got)
	}
	if err := h.call("open", map[string]string{"title": "A"}, nil); err == nil {
		t.Fatal("A still present")
	}
	var hist historyOut
	h.must(t, "history", map[string]string{"title": "B"}, &hist)
	if len(hist.Versions) != 1 {
		t.Fatalf("history of B = %+v", hist.Versions)
	}
}

// [P1] After an ordinary I/O failure leaves a journal pending, the next
// operation must first finish it (or refuse), never overwrite it.
func TestPendingJournalResolvedBeforeNextOperation(t *testing.T) {
	t.Run("transient", func(t *testing.T) {
		fs, h := setupPendingRename(t)
		failed := false
		fs.fail = func(method, p string) error {
			if method == "fs/save" && p == testVault+"/Linker.md" && !failed {
				failed = true
				return errors.New("io: transient EIO")
			}
			return nil
		}
		if err := h.call("rename", map[string]string{"title": "A", "new_title": "B"}, nil); err == nil {
			t.Fatal("injected failure did not surface")
		}
		if err := h.call("trash", map[string]string{"title": "C"}, nil); err != nil {
			t.Fatalf("trash after the failure is resolved: %v", err)
		}
		checkRenamed(t, h) // the same instance resynchronized
		h2 := mustReopen(t, fs)
		checkRenamed(t, h2)
		if _, ok := fs.get(testJournal); ok {
			t.Fatal("journal left behind")
		}
	})
	t.Run("persistent", func(t *testing.T) {
		fs, h := setupPendingRename(t)
		fs.fail = func(method, p string) error {
			if method == "fs/save" && p == testVault+"/Linker.md" {
				return errors.New("io: EIO")
			}
			return nil
		}
		if err := h.call("rename", map[string]string{"title": "A", "new_title": "B"}, nil); err == nil {
			t.Fatal("injected failure did not surface")
		}
		if err := h.call("trash", map[string]string{"title": "C"}, nil); err == nil {
			t.Error("a new journaled operation was accepted while the previous one is unresolved")
		}
		if err := h.call("save", map[string]string{"title": "C", "content": "c2\n"}, nil); err == nil {
			t.Error("a save was accepted while a journal is unresolved")
		}
		data, ok := fs.get(testJournal)
		if !ok || !strings.Contains(string(data), `"rename"`) {
			t.Fatalf("pending rename journal overwritten or lost: %s", data)
		}
		fs.fail = nil
		h2 := mustReopen(t, fs)
		checkRenamed(t, h2)
		if got := openContent(t, h2, "C"); got != "c\n" {
			t.Fatalf("C = %q", got)
		}
	})
}

func bodyField(t *testing.T, v sdk.View) string {
	t.Helper()
	for _, f := range v.Fields {
		if strings.HasPrefix(f.ID, "body") {
			return f.ID
		}
	}
	t.Fatalf("no body field in %+v", v.Fields)
	return ""
}

func hasAction(v sdk.View, id string) bool {
	for _, a := range v.Actions {
		if a.ID == id && !a.Disabled {
			return true
		}
	}
	return false
}

// [P1] Save in a clean but stale editor must not undo a save made through
// the routes; a dirty stale editor reports a conflict instead of
// overwriting, and overwriting is an explicit choice.
func TestEditorSaveDoesNotUndoRouteSave(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "A", "content": "v1\n"}, nil)
	v := h.view(t)
	v = h.act(t, "run", "", map[string]string{queryField(t, v): "open A"})
	body := bodyField(t, v)

	h.must(t, "save", map[string]string{"title": "A", "content": "v2\n"}, nil)
	v = h.act(t, "save", "", map[string]string{body: "v1\n"})
	if got := openContent(t, h, "A"); got != "v2\n" {
		t.Errorf("clean editor Save reverted a route save: content %q", got)
	}

	h.must(t, "save", map[string]string{"title": "A", "content": "v3\n"}, nil)
	v = h.act(t, "save", "", map[string]string{bodyField(t, v): "mine\n"})
	if got := openContent(t, h, "A"); got != "v3\n" {
		t.Fatalf("stale dirty editor silently overwrote a newer save: content %q", got)
	}
	if !hasAction(v, "overwrite") || !strings.Contains(strings.ToLower(v.Error), "changed") {
		t.Fatalf("no conflict surfaced: error=%q actions=%+v", v.Error, v.Actions)
	}
	if d := draftContents(t, h); d["A"] != "mine\n" {
		t.Fatalf("editor text not kept as a draft during the conflict: %+v", d)
	}
	v = h.act(t, "overwrite", "", nil)
	if got := openContent(t, h, "A"); got != "mine\n" {
		t.Fatalf("explicit overwrite = %q", got)
	}
}

func bigLinkNote(i int) []byte {
	var b strings.Builder
	for j := 0; b.Len() < 900<<10; j++ {
		fmt.Fprintf(&b, "[[%s %d %d]]\n", strings.Repeat("x", 170), i, j)
	}
	return []byte(b.String())
}

// [P1] The index is a cache: if it cannot be written or read within the
// IPC limits, the vault still opens from the notes and stays writable.
func TestOversizedIndexNeverBlocksNotes(t *testing.T) {
	fs := newMockFS()
	fs.maxFrame = 4 << 20
	h := openHarness(t, fs)
	h.must(t, "status", nil, nil)
	for i := 0; i < 5; i++ {
		fs.put(fmt.Sprintf("%s/Big %d.md", testVault, i), bigLinkNote(i))
	}
	fs.del(testIndex)
	h2 := mustReopen(t, fs)
	for i := 0; i < 5; i++ {
		h2.must(t, "open", map[string]string{"title": fmt.Sprintf("Big %d", i)}, nil)
	}
	if err := h2.call("create", map[string]string{"title": "Small", "content": "hi [[Big 0]]\n"}, nil); err != nil {
		t.Fatalf("create failed because the index cache could not be written: %v", err)
	}
	h3 := mustReopen(t, fs)
	h3.must(t, "open", map[string]string{"title": "Small"}, nil)
}

func TestOversizedFilesAreSkippedNotFatal(t *testing.T) {
	fs := newMockFS()
	fs.maxFrame = 4 << 20
	h := openHarness(t, fs)
	h.must(t, "create", map[string]string{"title": "Fine", "content": "ok\n"}, nil)
	fs.put(testVault+"/Huge.md", bytes.Repeat([]byte("h"), 5<<20))
	fs.put(testIndex, append([]byte(`{"version":1,"notes":{},"pad":"`), append(bytes.Repeat([]byte("p"), 5<<20), '"', '}')...))
	h2 := mustReopen(t, fs)
	if got := openContent(t, h2, "Fine"); got != "ok\n" {
		t.Fatalf("Fine = %q", got)
	}
	var st warnStatus
	h2.must(t, "status", nil, &st)
	if !strings.Contains(strings.Join(st.Index.Warnings, "\n"), "Huge.md") {
		t.Fatalf("oversized note not reported: %+v", st.Index.Warnings)
	}
	if _, ok := fs.get(testVault + "/Huge.md"); !ok {
		t.Fatal("oversized note was deleted; it must be left on disk")
	}
}

func zipOf(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func assertEmptyVault(t *testing.T, h *harness) {
	t.Helper()
	var st statusOut
	h.must(t, "status", nil, &st)
	if st.Notes != 0 || len(h.fs.paths(testVault+"/")) != 0 {
		t.Fatalf("rejected import left %d notes (%d files)", st.Notes, len(h.fs.paths(testVault+"/")))
	}
}

// [P1] ZIP import counts the bytes it actually inflates and rejects
// archives that blow past the per-entry, aggregate, or count limits,
// cleanly. (Hardening pass 2 dropped the compression-ratio rule, which
// refused ordinary repetitive notes; the "ratio" archive is now refused by
// the aggregate limit it also exceeds.)
func TestZipImportBombsRejected(t *testing.T) {
	t.Run("ratio", func(t *testing.T) {
		entries := map[string][]byte{}
		zeros := bytes.Repeat([]byte("0"), 1000000)
		for i := 0; i < 60; i++ {
			entries[fmt.Sprintf("bomb %02d.md", i)] = zeros
		}
		z := zipOf(t, entries)
		h := openHarness(t, newMockFS())
		if err := h.call("import", map[string]string{"zip_base64": z}, nil); err == nil {
			t.Errorf("a %d-byte archive inflating to %d MB was accepted", len(z)*3/4, 60)
		}
		assertEmptyVault(t, h)
	})
	t.Run("aggregate", func(t *testing.T) {
		entries := map[string][]byte{}
		for i := 0; i < 40; i++ {
			noise := make([]byte, 20<<10)
			rand.Read(noise)
			body := append([]byte(base64.StdEncoding.EncodeToString(noise)), bytes.Repeat([]byte(" "), 1000000-27308)...)
			entries[fmt.Sprintf("part %02d.md", i)] = body
		}
		z := zipOf(t, entries)
		h := openHarness(t, newMockFS())
		if err := h.call("import", map[string]string{"zip_base64": z}, nil); err == nil {
			t.Errorf("a %d-byte archive inflating to 40 MB was accepted", len(z)*3/4)
		}
		assertEmptyVault(t, h)
	})
	t.Run("lying_header", func(t *testing.T) {
		var raw bytes.Buffer
		fw, _ := flate.NewWriter(&raw, flate.BestCompression)
		fw.Write(bytes.Repeat([]byte("x"), 3<<20))
		fw.Close()
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.CreateRaw(&zip.FileHeader{Name: "liar.md", Method: zip.Deflate,
			CompressedSize64: uint64(raw.Len()), UncompressedSize64: 10})
		if err != nil {
			t.Fatal(err)
		}
		w.Write(raw.Bytes())
		zw.Close()
		h := openHarness(t, newMockFS())
		_ = h.call("import", map[string]string{"zip_base64": base64.StdEncoding.EncodeToString(buf.Bytes())}, nil)
		assertEmptyVault(t, h)
	})
}

// Restoring a trashed note whose title now has a pending draft must not
// strand a journal (the trashed draft would collide with the new one).
func TestRestoreIntoPendingDraftTitle(t *testing.T) {
	fs := newMockFS()
	h := openHarness(t, fs)
	h.must(t, "create", map[string]string{"title": "A", "content": "a\n"}, nil)
	h.must(t, "edit", map[string]string{"title": "A", "content": "a draft\n"}, nil)
	var tr struct {
		TrashID string `json:"trash_id"`
	}
	h.must(t, "trash", map[string]string{"title": "A"}, &tr)
	h.must(t, "edit", map[string]string{"title": "A", "content": "new unsaved\n"}, nil)
	_ = h.call("restore", map[string]string{"trash_id": tr.TrashID}, nil)
	h2 := mustReopen(t, fs)
	found := false
	for _, c := range draftContents(t, h2) {
		found = found || c == "new unsaved\n"
	}
	if !found {
		t.Fatalf("pending draft lost: %+v", draftContents(t, h2))
	}
	if _, ok := fs.get(testJournal); ok {
		t.Fatal("journal left behind")
	}
}

// Re-staging a draft under a different capitalization must replace the old
// draft file, not leave a stale one that wins on the next open.
func TestDraftRestagedWithOtherCaseReplacesOld(t *testing.T) {
	fs := newMockFS()
	h := openHarness(t, fs)
	h.must(t, "edit", map[string]string{"title": "b", "content": "old\n"}, nil)
	h.must(t, "edit", map[string]string{"title": "B", "content": "new\n"}, nil)
	h2 := mustReopen(t, fs)
	d := draftContents(t, h2)
	if len(d) != 1 {
		t.Fatalf("drafts = %+v", d)
	}
	for _, c := range d {
		if c != "new\n" {
			t.Fatalf("stale draft won after reopen: %q", c)
		}
	}
}
