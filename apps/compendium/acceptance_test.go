package compendium

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gostalgia/sdk"
)

// Acceptance 1: writing [[B]] in A makes B show A in its backlinks after save.
func TestAcceptanceCreateLinkBacklink(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "A"}, nil)
	h.must(t, "create", map[string]string{"title": "B", "content": "I am B.\n"}, nil)

	var b noteOut
	h.must(t, "open", map[string]string{"title": "B"}, &b)
	if len(b.Backlinks) != 0 {
		t.Fatalf("B backlinks before link = %v, want none", b.Backlinks)
	}

	h.must(t, "save", map[string]string{"title": "A", "content": "Alpha points to [[B]] today.\n"}, nil)
	h.must(t, "open", map[string]string{"title": "B"}, &b)
	if got := titles(b.Backlinks); !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("B backlinks after save = %v, want [A]", got)
	}
	if !strings.Contains(b.Backlinks[0].Context, "[[B]]") {
		t.Fatalf("backlink context %q should quote the linking line", b.Backlinks[0].Context)
	}

	var a noteOut
	h.must(t, "open", map[string]string{"title": "A"}, &a)
	if len(a.Links) != 1 || a.Links[0].Target != "B" || !a.Links[0].Resolved {
		t.Fatalf("A links = %+v, want one resolved link to B", a.Links)
	}

	// Removing the link removes the backlink, and it survives a relaunch.
	h.must(t, "save", map[string]string{"title": "A", "content": "No links now.\n"}, nil)
	h2 := openHarness(t, h.fs)
	h2.must(t, "open", map[string]string{"title": "B"}, &b)
	if len(b.Backlinks) != 0 {
		t.Fatalf("B backlinks after unlink+relaunch = %v, want none", b.Backlinks)
	}
}

// Acceptance 2: renaming B rewrites every [[B]] vault-wide; links that
// cannot be rewritten (in trashed notes) are reported, never left silently.
func TestAcceptanceRenamePropagates(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "B", "content": "B links to itself: [[B]]\n"}, nil)
	h.must(t, "create", map[string]string{"title": "A", "content": "See [[B]], or [[b|the bee]], or [[B#Section]].\n`[[B]]` in code is not a link.\n"}, nil)
	h.must(t, "create", map[string]string{"title": "C", "content": "- [[B]]\n- [[Other]]\n"}, nil)
	h.must(t, "create", map[string]string{"title": "T", "content": "Trashed note mentions [[B]].\n"}, nil)
	var tr struct {
		TrashID string `json:"trash_id"`
	}
	h.must(t, "trash", map[string]string{"title": "T"}, &tr)

	var r renameOut
	h.must(t, "rename", map[string]string{"title": "B", "new_title": "Beta Notes"}, &r)
	if r.From != "B" || r.To != "Beta Notes" {
		t.Fatalf("rename result = %+v", r)
	}
	sort.Strings(r.RewrittenNotes)
	if !reflect.DeepEqual(r.RewrittenNotes, []string{"A", "Beta Notes", "C"}) {
		t.Fatalf("rewritten notes = %v, want [A Beta Notes C]", r.RewrittenNotes)
	}
	if r.RewrittenLinks != 5 {
		t.Fatalf("rewritten links = %d, want 5", r.RewrittenLinks)
	}
	if len(r.Orphaned) != 1 || r.Orphaned[0].Note != "T" || r.Orphaned[0].Location != "trash" {
		t.Fatalf("orphaned = %+v, want the trashed note T reported", r.Orphaned)
	}

	var a noteOut
	h.must(t, "open", map[string]string{"title": "A"}, &a)
	want := "See [[Beta Notes]], or [[Beta Notes|the bee]], or [[Beta Notes#Section]].\n`[[B]]` in code is not a link.\n"
	if a.Content != want {
		t.Fatalf("A after rename:\n%q\nwant\n%q", a.Content, want)
	}
	var beta noteOut
	h.must(t, "open", map[string]string{"title": "Beta Notes"}, &beta)
	if got := titles(beta.Backlinks); !reflect.DeepEqual(got, []string{"A", "Beta Notes", "C"}) {
		t.Fatalf("Beta backlinks = %v", got)
	}
	if err := h.call("open", map[string]string{"title": "B"}, nil); err == nil {
		t.Fatal("old title B still opens after rename")
	}

	// No live note dangles on the old title; the graph still reports the
	// genuinely missing [[Other]] target instead of hiding it.
	var g graphOut
	h.must(t, "graph", nil, &g)
	for _, u := range g.Unresolved {
		if u.Target == "B" {
			t.Fatalf("live link silently dangling on renamed title: %+v", u)
		}
	}
	if len(g.Unresolved) != 1 || g.Unresolved[0].Target != "Other" {
		t.Fatalf("unresolved = %+v, want [[Other]] reported", g.Unresolved)
	}

	// Renaming onto an existing title is refused and changes nothing.
	if err := h.call("rename", map[string]string{"title": "A", "new_title": "c"}, nil); err == nil {
		t.Fatal("rename onto existing (case-insensitive) title succeeded")
	}
	// Survives relaunch.
	h2 := openHarness(t, h.fs)
	h2.must(t, "open", map[string]string{"title": "Beta Notes"}, &beta)
	if len(beta.Backlinks) != 3 {
		t.Fatalf("backlinks after relaunch = %v", beta.Backlinks)
	}
}

// Acceptance 3 (bound): ranked search with snippets over 2,000 notes stays
// well under 100ms per query. BenchmarkSearch2000 reports the steady state.
func TestAcceptanceSearchUnder100ms(t *testing.T) {
	h := syntheticVault(t, 2000)
	var st statusOut
	h.must(t, "status", nil, &st)
	if st.Notes != 2000 {
		t.Fatalf("notes = %d, want 2000", st.Notes)
	}
	queries := []string{"lantern", "quiet harbor", "atlas", "river stone", "zephyr", "orchard#", "compass ridge meadow", "lan", "note 1999", "sol"}
	var worst float64
	for round := 0; round < 5; round++ {
		for _, q := range queries {
			var out searchOut
			h.must(t, "search", map[string]any{"query": q, "limit": 20}, &out)
			if out.TookMS > worst {
				worst = out.TookMS
			}
			if q == "zephyr" {
				continue // vocabulary miss: zero results is the right answer
			}
			if len(out.Results) == 0 {
				t.Fatalf("query %q returned no results", q)
			}
			for i, r := range out.Results {
				if r.Snippet == "" {
					t.Fatalf("query %q result %q has no snippet", q, r.Title)
				}
				if i > 0 && r.Score > out.Results[i-1].Score {
					t.Fatalf("query %q results not ranked: %v > %v", q, r.Score, out.Results[i-1].Score)
				}
			}
		}
	}
	t.Logf("worst search latency over 2,000 notes: %.3fms", worst)
	if worst >= 100 {
		t.Fatalf("search took %.3fms, bound is 100ms", worst)
	}

	// Ranking sanity: the note whose title matches outranks body mentions,
	// and snippets carry the matched text.
	var out searchOut
	h.must(t, "search", map[string]any{"query": "note 1999"}, &out)
	if out.Results[0].Title != "Note 1999" {
		t.Fatalf("top hit for 'note 1999' = %q", out.Results[0].Title)
	}
	h.must(t, "search", map[string]any{"query": "lantern"}, &out)
	if !strings.Contains(strings.ToLower(out.Results[0].Snippet), "lantern") {
		t.Fatalf("snippet %q does not contain the match", out.Results[0].Snippet)
	}
}

// Acceptance 4: delete the index, reopen, full rebuild from the Markdown
// sources with zero content loss.
func TestAcceptanceIndexRebuild(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "Hub", "content": "Links: [[Spoke 1]] [[Spoke 2]] #hub\n"}, nil)
	h.must(t, "create", map[string]string{"title": "Spoke 1", "content": "Back to [[Hub]] #spoke\n"}, nil)
	h.must(t, "create", map[string]string{"title": "Spoke 2", "content": "Also [[Hub]] and [[Nowhere]] #spoke #two\n"}, nil)
	before := snapshotVault(t, h)
	if _, ok := h.fs.get(testIndex); !ok {
		t.Fatal("index.json was never persisted")
	}

	for _, damage := range []string{"delete", "corrupt", "stale"} {
		fs := h.fs.clone()
		switch damage {
		case "delete":
			fs.del(testIndex)
		case "corrupt":
			fs.put(testIndex, []byte(`{"version":1,"notes":{"Hub.md":`))
		case "stale":
			fs.put(testIndex, []byte(`{"version":1,"notes":{"Hub.md":{"title":"Hub","size":3,"sha256":"00","links":["Ghost"],"tags":["ghost"]}}}`))
		}
		h2 := openHarness(t, fs)
		var st statusOut
		h2.must(t, "status", nil, &st)
		if !st.Index.Rebuilt || st.Index.Reparsed != 3 {
			t.Fatalf("%s: status.index = %+v, want rebuilt with 3 reparsed", damage, st.Index)
		}
		if after := snapshotVault(t, h2); !reflect.DeepEqual(before, after) {
			t.Fatalf("%s: rebuilt vault differs:\nbefore %+v\nafter  %+v", damage, before, after)
		}
		// The rebuilt index was persisted: the next open reparses nothing.
		h3 := openHarness(t, fs)
		h3.must(t, "status", nil, &st)
		if st.Index.Rebuilt || st.Index.Reparsed != 0 {
			t.Fatalf("%s: third open status.index = %+v, want clean", damage, st.Index)
		}
	}

	// Explicit rebuild route.
	var rb struct {
		Notes    int `json:"notes"`
		Reparsed int `json:"reparsed"`
	}
	h.must(t, "rebuild", nil, &rb)
	if rb.Notes != 3 || rb.Reparsed != 3 {
		t.Fatalf("rebuild = %+v", rb)
	}
}

// Acceptance 5: a kill at every step of a save (before/after each fs call,
// torn staging file, failed atomic rename) never loses committed content or
// corrupts the index; the interrupted edit is recoverable or cleanly absent.
func TestAcceptanceCrashMidSave(t *testing.T) {
	const oldBody = "Old body links [[Beta]] #old\n"
	const newBody = "New body links [[Gamma]] #new\nwith a second line.\n"
	setup := func(h *harness) {
		h.must(t, "create", map[string]string{"title": "Beta"}, nil)
		h.must(t, "create", map[string]string{"title": "Gamma"}, nil)
		h.must(t, "create", map[string]string{"title": "Alpha", "content": oldBody}, nil)
		h.must(t, "edit", map[string]string{"title": "Alpha", "content": newBody}, nil)
	}
	op := func(h *harness) error {
		return h.call("save", map[string]string{"title": "Alpha", "content": newBody}, nil)
	}
	steps := crashMatrix(t, setup, op, func(t *testing.T, h *harness) {
		var a noteOut
		h.must(t, "open", map[string]string{"title": "Alpha"}, &a)
		var d draftsOut
		h.must(t, "drafts", nil, &d)
		var gamma, beta noteOut
		h.must(t, "open", map[string]string{"title": "Gamma"}, &gamma)
		h.must(t, "open", map[string]string{"title": "Beta"}, &beta)
		switch a.Content {
		case oldBody:
			// Not committed: the edit must still be recoverable.
			if len(d.Drafts) != 1 || d.Drafts[0].Title != "Alpha" || d.Drafts[0].Content != newBody {
				t.Fatalf("save not committed but draft lost: %+v", d.Drafts)
			}
			if !contains(titles(beta.Backlinks), "Alpha") || contains(titles(gamma.Backlinks), "Alpha") {
				t.Fatal("index inconsistent with old content")
			}
		case newBody:
			// Committed: the draft is cleanly absent.
			if len(d.Drafts) != 0 {
				t.Fatalf("save committed but stale draft remains: %+v", d.Drafts)
			}
			if contains(titles(beta.Backlinks), "Alpha") || !contains(titles(gamma.Backlinks), "Alpha") {
				t.Fatal("index inconsistent with new content")
			}
		default:
			t.Fatalf("committed content corrupted: %q", a.Content)
		}
		// Recovering the draft and saving converges on the new body.
		if a.Content == oldBody {
			h.must(t, "recover", map[string]any{"title": "Alpha", "restore": true}, nil)
			h.must(t, "open", map[string]string{"title": "Alpha"}, &a)
			if a.Content != newBody {
				t.Fatalf("recovered content = %q", a.Content)
			}
		}
	})
	if steps < 3 {
		t.Fatalf("save performed only %d mutating steps; matrix is not exercising it", steps)
	}
}

// Acceptance 6: trash then restore preserves links, tags, and history.
func TestAcceptanceTrashRestoreRoundTrip(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "Hub", "content": "Links to [[Leaf]].\n"}, nil)
	h.must(t, "create", map[string]string{"title": "Leaf", "content": "v1 #green\n"}, nil)
	h.must(t, "save", map[string]string{"title": "Leaf", "content": "v2 [[Hub]] #green\n"}, nil)
	h.must(t, "save", map[string]string{"title": "Leaf", "content": "v3 [[Hub]] #green #leafy\n"}, nil)
	var leaf noteOut
	h.must(t, "open", map[string]string{"title": "Leaf"}, &leaf)
	var hist historyOut
	h.must(t, "history", map[string]string{"title": "Leaf"}, &hist)
	if len(hist.Versions) != 2 {
		t.Fatalf("history before trash = %d versions, want 2", len(hist.Versions))
	}

	var tr struct {
		TrashID string `json:"trash_id"`
	}
	h.must(t, "trash", map[string]string{"title": "Leaf"}, &tr)
	if err := h.call("open", map[string]string{"title": "Leaf"}, nil); err == nil {
		t.Fatal("trashed note still opens")
	}
	var hub noteOut
	h.must(t, "open", map[string]string{"title": "Hub"}, &hub)
	if len(hub.Links) != 1 || hub.Links[0].Resolved {
		t.Fatalf("link to trashed note should show as broken: %+v", hub.Links)
	}
	var tl trashListOut
	h.must(t, "trash_list", nil, &tl)
	if len(tl.Items) != 1 || tl.Items[0].Title != "Leaf" || tl.Items[0].TrashID != tr.TrashID {
		t.Fatalf("trash list = %+v", tl.Items)
	}

	// Restore in a fresh launch to prove it is durable state, not memory.
	h2 := openHarness(t, h.fs)
	h2.must(t, "restore", map[string]string{"trash_id": tr.TrashID}, nil)
	var back noteOut
	h2.must(t, "open", map[string]string{"title": "Leaf"}, &back)
	if back.Content != leaf.Content || !reflect.DeepEqual(back.Tags, leaf.Tags) ||
		!reflect.DeepEqual(back.Links, leaf.Links) || !reflect.DeepEqual(titles(back.Backlinks), titles(leaf.Backlinks)) {
		t.Fatalf("restored note differs:\n%+v\nwant\n%+v", back, leaf)
	}
	var hist2 historyOut
	h2.must(t, "history", map[string]string{"title": "Leaf"}, &hist2)
	if !reflect.DeepEqual(hist, hist2) {
		t.Fatalf("history not preserved:\n%+v\nwant\n%+v", hist2, hist)
	}
	h2.must(t, "open", map[string]string{"title": "Hub"}, &hub)
	if !hub.Links[0].Resolved {
		t.Fatal("link to restored note did not resolve again")
	}
	h2.must(t, "trash_list", nil, &tl)
	if len(tl.Items) != 0 {
		t.Fatalf("trash not empty after restore: %+v", tl.Items)
	}
	var tags struct {
		Tags []struct {
			Tag   string `json:"tag"`
			Count int    `json:"count"`
		} `json:"tags"`
	}
	h2.must(t, "tags", nil, &tags)
	if len(tags.Tags) != 2 {
		t.Fatalf("tags after restore = %+v", tags.Tags)
	}
}

// Acceptance 7: export is byte-identical Markdown for every note, single
// or as a vault zip, and re-importing the zip round-trips exactly.
func TestAcceptanceExportByteIdentical(t *testing.T) {
	corpus := map[string]string{
		"Plain":         "# Plain\n\nJust text.\n",
		"CRLF":          "line one\r\nline two\r\n",
		"No Newline":    "no trailing newline",
		"Unicode 日本語":   "Ünïcødé — 日本語 🚀 [[Plain]] #étiquette\n",
		"Tabs & Spaces": "\tindented\n  trailing spaces   \n\n\n",
		"BOM":           "\ufeffbyte order mark first\n",
		"Empty":         "",
		"Colon: A*B?":   "title with reserved characters\n",
		".hidden":       "leading dot title\n",
	}
	h := openHarness(t, newMockFS())
	for title, body := range corpus {
		h.must(t, "import", map[string]string{"title": title, "content": body}, nil)
	}
	for title, body := range corpus {
		var ex exportOut
		h.must(t, "export", map[string]string{"title": title}, &ex)
		data, err := base64.StdEncoding.DecodeString(ex.DataBase64)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, []byte(body)) {
			t.Fatalf("export of %q = %q, want %q", title, data, body)
		}
		if stored, ok := h.fs.get(ex.Path); !ok || !bytes.Equal(stored, data) {
			t.Fatalf("export file %s missing or differs", ex.Path)
		}
	}

	var ex exportOut
	h.must(t, "export", map[string]string{}, &ex)
	if ex.Files != len(corpus) {
		t.Fatalf("vault export files = %d, want %d", ex.Files, len(corpus))
	}
	zipData, _ := base64.StdEncoding.DecodeString(ex.DataBase64)
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(corpus) {
		t.Fatalf("zip entries = %d", len(zr.File))
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".md") || strings.Contains(f.Name, "/") {
			t.Fatalf("zip entry name %q is not a flat .md file", f.Name)
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		found := false
		for _, body := range corpus {
			found = found || bytes.Equal(b, []byte(body))
		}
		if !found {
			t.Fatalf("zip entry %s content %q matches no note", f.Name, b)
		}
	}

	// Round trip: import the zip into an empty vault.
	fresh := openHarness(t, newMockFS())
	var imp struct {
		Imported []string `json:"imported"`
	}
	fresh.must(t, "import", map[string]string{"zip_base64": ex.DataBase64}, &imp)
	if len(imp.Imported) != len(corpus) {
		t.Fatalf("imported %d notes, want %d: %v", len(imp.Imported), len(corpus), imp.Imported)
	}
	for title, body := range corpus {
		var n noteOut
		fresh.must(t, "open", map[string]string{"title": title}, &n)
		if n.Content != body {
			t.Fatalf("round-trip %q = %q, want %q", title, n.Content, body)
		}
	}
}

// Broken links render as broken and offer one-key creation of the target.
func TestBrokenLinkOneKeyCreate(t *testing.T) {
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "Start", "content": "Go to [[Exists]] or [[Missing Page]].\n"}, nil)
	h.must(t, "create", map[string]string{"title": "Exists"}, nil)
	var n noteOut
	h.must(t, "open", map[string]string{"title": "Start"}, &n)
	if !strings.Contains(n.Rendered, "Missing Page") || !strings.Contains(n.Rendered, "missing") {
		t.Fatalf("rendered text does not flag the broken link: %q", n.Rendered)
	}

	h.view(t)
	v := h.act(t, "run", "", map[string]string{queryField(t, h.view(t)): "open Start"})
	var broken string
	for _, it := range v.Items {
		if strings.Contains(it.Label, "Missing Page") && strings.Contains(strings.ToLower(it.Label+it.Detail), "missing") {
			broken = it.ID
		}
	}
	if broken == "" {
		t.Fatalf("no broken-link item in note view: %+v", v.Items)
	}
	v = h.act(t, "follow", broken, nil)
	if !strings.Contains(v.Title, "Missing Page") {
		t.Fatalf("follow on broken link did not open the created note: %q", v.Title)
	}
	h.must(t, "open", map[string]string{"title": "Missing Page"}, &n)
	h.must(t, "open", map[string]string{"title": "Start"}, &n)
	for _, l := range n.Links {
		if !l.Resolved {
			t.Fatalf("link %q still broken after one-key create", l.Target)
		}
	}
}

// queryField finds the home view's search/command field ID.
func queryField(t *testing.T, v sdk.View) string {
	t.Helper()
	for _, f := range v.Fields {
		if strings.HasPrefix(f.ID, "q") {
			return f.ID
		}
	}
	t.Fatalf("no query field in view %q: %+v", v.Title, v.Fields)
	return ""
}

type vaultSnapshot struct {
	Notes map[string]string
	Back  map[string][]string
	Graph graphOut
	Tags  string
}

func snapshotVault(t *testing.T, h *harness) vaultSnapshot {
	t.Helper()
	var list struct {
		Notes []struct {
			Title string `json:"title"`
		} `json:"notes"`
	}
	h.must(t, "list", nil, &list)
	s := vaultSnapshot{Notes: map[string]string{}, Back: map[string][]string{}}
	for _, n := range list.Notes {
		var out noteOut
		h.must(t, "open", map[string]string{"title": n.Title}, &out)
		s.Notes[n.Title] = out.Content
		s.Back[n.Title] = titles(out.Backlinks)
	}
	h.must(t, "graph", nil, &s.Graph)
	var tags any
	h.must(t, "tags", nil, &tags)
	b, _ := jsonString(tags)
	s.Tags = b
	return s
}
