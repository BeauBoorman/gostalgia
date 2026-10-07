package compendium

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// crashMatrix runs op once to count its mutating fs calls, then replays it
// against a fresh copy of the same starting state once per (step, crash
// mode), kills the "process" at that step, reopens a new instance on the
// surviving state, and runs check plus the generic index invariants. It
// returns the number of mutating steps op performs.
func crashMatrix(t *testing.T, setup func(*harness), op func(*harness) error, check func(*testing.T, *harness)) int {
	t.Helper()
	seed := openHarness(t, newMockFS())
	setup(seed)
	base := seed.fs.clone()

	dry := openHarness(t, base.clone())
	dry.must(t, "status", nil, nil)
	ops0 := dry.fs.ops
	if err := op(dry); err != nil {
		t.Fatalf("operation fails without a crash: %v", err)
	}
	total := dry.fs.ops - ops0
	for k := 1; k <= total; k++ {
		for _, mode := range []crashMode{crashBefore, crashAfter, crashTorn, crashRecover} {
			fs := base.clone()
			victim := openHarness(t, fs)
			victim.must(t, "status", nil, nil)
			fs.crashAt, fs.mode = fs.ops+k, mode
			_ = op(victim)
			fs.dead, fs.crashAt = false, 0

			t.Run(fmt.Sprintf("step%02d_%s", k, mode), func(t *testing.T) {
				reopened := openHarness(t, fs)
				reopened.must(t, "status", nil, nil)
				for _, p := range fs.paths(testRoot) {
					if strings.Contains(p, ".tmp.") || strings.HasSuffix(p, ".recover") {
						t.Fatalf("interrupted-save artifact left behind after reopen: %s", p)
					}
				}
				check(t, reopened)
				assertIndexConsistent(t, fs)
			})
		}
	}
	return total
}

// assertIndexConsistent proves the persisted index agrees with the Markdown
// sources: reopening reparses nothing, and a from-scratch rebuild (index
// deleted) yields the identical vault view.
func assertIndexConsistent(t *testing.T, fs *mockFS) {
	t.Helper()
	withIndex := openHarness(t, fs.clone())
	var st statusOut
	withIndex.must(t, "status", nil, &st)
	if st.Index.Rebuilt || st.Index.Reparsed != 0 || st.Index.JournalReplayed {
		t.Fatalf("persisted index inconsistent with sources: %+v", st.Index)
	}
	scratch := fs.clone()
	scratch.del(testIndex)
	rebuilt := openHarness(t, scratch)
	if a, b := snapshotVault(t, withIndex), snapshotVault(t, rebuilt); !reflect.DeepEqual(a, b) {
		t.Fatalf("index view differs from full rebuild:\nindex   %+v\nrebuild %+v", a, b)
	}
}

func TestCrashDuringRenameIsAllOrNothing(t *testing.T) {
	setup := func(h *harness) {
		h.must(t, "create", map[string]string{"title": "B", "content": "self [[B]]\n"}, nil)
		h.must(t, "save", map[string]string{"title": "B", "content": "self [[B]] v2\n"}, nil) // history to move
		h.must(t, "create", map[string]string{"title": "A", "content": "to [[B]] and [[b|bee]]\n"}, nil)
		h.must(t, "create", map[string]string{"title": "C", "content": "[[B]]\n"}, nil)
		h.must(t, "edit", map[string]string{"title": "C", "content": "[[B]] draft\n"}, nil)
	}
	op := func(h *harness) error {
		return h.call("rename", map[string]string{"title": "B", "new_title": "Beta"}, nil)
	}
	steps := crashMatrix(t, setup, op, func(t *testing.T, h *harness) {
		var a, c noteOut
		h.must(t, "open", map[string]string{"title": "A"}, &a)
		h.must(t, "open", map[string]string{"title": "C"}, &c)
		var d draftsOut
		h.must(t, "drafts", nil, &d)
		oldErr := h.call("open", map[string]string{"title": "B"}, nil)
		newErr := h.call("open", map[string]string{"title": "Beta"}, nil)
		var hist historyOut
		switch {
		case oldErr == nil && newErr != nil:
			if a.Content != "to [[B]] and [[b|bee]]\n" || c.Content != "[[B]]\n" {
				t.Fatalf("rename not committed but links were rewritten: %q %q", a.Content, c.Content)
			}
			if len(d.Drafts) != 1 || d.Drafts[0].Content != "[[B]] draft\n" {
				t.Fatalf("draft changed by uncommitted rename: %+v", d.Drafts)
			}
			h.must(t, "history", map[string]string{"title": "B"}, &hist)
		case oldErr != nil && newErr == nil:
			if a.Content != "to [[Beta]] and [[Beta|bee]]\n" || c.Content != "[[Beta]]\n" {
				t.Fatalf("rename committed but links not fully rewritten: %q %q", a.Content, c.Content)
			}
			if len(d.Drafts) != 1 || d.Drafts[0].Content != "[[Beta]] draft\n" {
				t.Fatalf("pending draft not rewritten with rename: %+v", d.Drafts)
			}
			h.must(t, "history", map[string]string{"title": "Beta"}, &hist)
		default:
			t.Fatalf("rename half-applied: open B err=%v, open Beta err=%v", oldErr, newErr)
		}
		if len(hist.Versions) != 1 {
			t.Fatalf("history lost across interrupted rename: %+v", hist.Versions)
		}
	})
	t.Logf("rename exercised %d mutating steps x 4 crash modes", steps)
}

func TestCrashDuringTrashAndRestore(t *testing.T) {
	setup := func(h *harness) {
		h.must(t, "create", map[string]string{"title": "Hub", "content": "[[Leaf]]\n"}, nil)
		h.must(t, "create", map[string]string{"title": "Leaf", "content": "v1 #t\n"}, nil)
		h.must(t, "save", map[string]string{"title": "Leaf", "content": "v2 [[Hub]] #t\n"}, nil)
	}
	checkLeaf := func(t *testing.T, h *harness) {
		var tl trashListOut
		h.must(t, "trash_list", nil, &tl)
		live := h.call("open", map[string]string{"title": "Leaf"}, nil) == nil
		switch {
		case live && len(tl.Items) == 0:
		case !live && len(tl.Items) == 1 && tl.Items[0].Title == "Leaf":
			h.must(t, "restore", map[string]string{"trash_id": tl.Items[0].TrashID}, nil)
		default:
			t.Fatalf("Leaf half-trashed: live=%v trash=%+v", live, tl.Items)
		}
		var leaf noteOut
		h.must(t, "open", map[string]string{"title": "Leaf"}, &leaf)
		var hist historyOut
		h.must(t, "history", map[string]string{"title": "Leaf"}, &hist)
		if leaf.Content != "v2 [[Hub]] #t\n" || len(hist.Versions) != 1 {
			t.Fatalf("Leaf content/history damaged: %q, %d versions", leaf.Content, len(hist.Versions))
		}
	}
	t.Run("trash", func(t *testing.T) {
		crashMatrix(t, setup, func(h *harness) error {
			return h.call("trash", map[string]string{"title": "Leaf"}, nil)
		}, checkLeaf)
	})
	t.Run("restore", func(t *testing.T) {
		trashed := func(h *harness) {
			setup(h)
			h.must(t, "trash", map[string]string{"title": "Leaf"}, nil)
		}
		crashMatrix(t, trashed, func(h *harness) error {
			var tl trashListOut
			if err := h.call("trash_list", nil, &tl); err != nil {
				return err
			}
			return h.call("restore", map[string]string{"trash_id": tl.Items[0].TrashID}, nil)
		}, checkLeaf)
	})
}

func TestCrashDuringCreateAndImport(t *testing.T) {
	setup := func(h *harness) {
		h.must(t, "create", map[string]string{"title": "Existing", "content": "[[New]]\n"}, nil)
	}
	crashMatrix(t, setup, func(h *harness) error {
		return h.call("create", map[string]string{"title": "New", "content": "fresh [[Existing]]\n"}, nil)
	}, func(t *testing.T, h *harness) {
		var n noteOut
		if err := h.call("open", map[string]string{"title": "New"}, &n); err == nil && n.Content != "fresh [[Existing]]\n" {
			t.Fatalf("created note torn: %q", n.Content)
		}
	})
}

func TestCrashDuringIndexRebuild(t *testing.T) {
	setup := func(h *harness) {
		h.must(t, "create", map[string]string{"title": "One", "content": "[[Two]] #a\n"}, nil)
		h.must(t, "create", map[string]string{"title": "Two", "content": "[[One]] #b\n"}, nil)
	}
	crashMatrix(t, setup, func(h *harness) error {
		return h.call("rebuild", nil, nil)
	}, func(t *testing.T, h *harness) {
		var one noteOut
		h.must(t, "open", map[string]string{"title": "One"}, &one)
		if one.Content != "[[Two]] #a\n" || len(one.Backlinks) != 1 {
			t.Fatalf("rebuild crash damaged One: %+v", one)
		}
	})
}
