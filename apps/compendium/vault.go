package compendium

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Storage layout, entirely inside the app-private partition. Markdown files
// under vault/ are the source of truth; everything else is derived from
// them or exists only to make multi-file operations recoverable.
const (
	rootDir     = "/apps/data/" + ID
	vaultDir    = rootDir + "/vault"   // <stem>.md, one per live note
	historyDir  = rootDir + "/history" // <stem>/<seq>.md, prior versions
	draftsDir   = rootDir + "/drafts"  // <stem>.json, unsaved edits
	trashDir    = rootDir + "/trash"   // <id>/<stem>.md (+history/, draft.json)
	exportsDir  = rootDir + "/exports" // last exports
	indexPath   = rootDir + "/index.json"
	journalPath = rootDir + "/journal.json"

	// MaxNoteBytes bounds one note.
	MaxNoteBytes = 1 << 20
	// MaxNotes bounds the vault so memory and the index stay bounded.
	MaxNotes = 10000
	// MaxHistory is the number of prior versions kept per note.
	MaxHistory = 20
	// maxJournalData bounds the bytes one journaled operation may rewrite,
	// keeping the journal under the 4 MiB IPC write limit after base64.
	maxJournalData = 2 << 20
	// maxExportBytes keeps exports under the IPC frame limit after base64.
	maxExportBytes = 2900 << 10
	// maxIndexBytes bounds the persisted index the same way. The index is a
	// cache: when it would be larger, it is simply not persisted and every
	// open reparses the notes instead.
	maxIndexBytes = 2900 << 10
	// MaxVaultBytes bounds the total size of the notes held in memory.
	MaxVaultBytes = 256 << 20

	indexVersion = 1
)

// note is one live note held in memory.
type note struct {
	doc     int32
	title   string
	stem    string
	content string
	sha     string
	links   []string
	tags    []string
}

func (n *note) file() string { return vaultDir + "/" + n.stem + ".md" }

// draft is an unsaved edit staged in app-private storage.
type draft struct {
	Title    string    `json:"title"`
	Content  string    `json:"content"`
	BaseSHA  string    `json:"base_sha256"`
	StagedAt time.Time `json:"staged_at"`
}

type trashEntry struct {
	ID    string
	Title string
	Stem  string
}

type indexEntry struct {
	Title  string   `json:"title"`
	Size   int      `json:"size"`
	SHA256 string   `json:"sha256"`
	Links  []string `json:"links"`
	Tags   []string `json:"tags"`
}

type indexFile struct {
	Version int                   `json:"version"`
	Notes   map[string]indexEntry `json:"notes"`
}

// jop is one idempotent journal step.
type jop struct {
	Op   string `json:"op"` // move | write | remove
	Src  string `json:"src,omitempty"`
	Dst  string `json:"dst,omitempty"`
	Path string `json:"path,omitempty"`
	Data []byte `json:"data,omitempty"`
}

type journal struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Ops     []jop  `json:"ops"`
}

type loadStats struct {
	Rebuilt         bool     `json:"rebuilt"`
	Reparsed        int      `json:"reparsed"`
	JournalReplayed bool     `json:"journal_replayed"`
	Recovered       int      `json:"recovered_artifacts"`
	Skipped         int      `json:"skipped_files,omitempty"`
	IndexProblem    string   `json:"index_problem,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

func (s *loadStats) warn(format string, args ...any) {
	if len(s.Warnings) < 50 {
		s.Warnings = append(s.Warnings, fmt.Sprintf(format, args...))
	}
}

// vault is the in-memory model of the notes plus the operations that keep
// storage and memory in step. Callers hold Compendium.mu.
type vault struct {
	st      store
	now     func() time.Time
	notes   map[string]*note // fold(title) -> note
	byDoc   map[int32]*note
	nextDoc int32
	back    map[string]map[string]bool // fold(target) -> fold(source) set
	tagIdx  map[string]map[string]bool // tag -> fold(title) set
	search  *searchIndex
	drafts  map[string]*draft // fold(title) -> draft
	trash   []trashEntry
	stats   loadStats
	bytes   int // total size of loaded notes
	// norm finds a note by normKey(title): titles that differ only by
	// Unicode normalization would share one file on APFS.
	norm map[string]*note
	// failedSaves holds draft files whose last save failed in this
	// process: such a save may have left a .recover artifact beside the
	// file, which the next successful save of that draft supersedes.
	failedSaves map[string]bool

	// pending is set when a journaled operation failed part-way with the
	// process still running. No further change is accepted until ready()
	// has finished that operation and resynchronized memory with storage.
	pending bool
}

func newVault(st store, now func() time.Time) *vault {
	return &vault{st: st, now: now}
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

var stagingName = regexp.MustCompile(`\.tmp\.[0-9]+\.[0-9]+$`)

// load (re)builds memory from storage: finish any journaled operation,
// sweep interrupted-save artifacts, verify the persisted index against the
// sources by content hash, and reparse whatever does not match.
func (v *vault) load(ctx context.Context, ignoreIndex bool) error {
	v.notes, v.byDoc, v.nextDoc = map[string]*note{}, map[int32]*note{}, 0
	v.back, v.tagIdx = map[string]map[string]bool{}, map[string]map[string]bool{}
	v.search, v.drafts, v.trash = newSearchIndex(), map[string]*draft{}, nil
	v.stats, v.bytes, v.pending = loadStats{}, 0, false
	v.norm, v.failedSaves = map[string]*note{}, map[string]bool{}

	if err := v.ensureLayout(ctx); err != nil {
		return err
	}
	if err := v.replayJournal(ctx); err != nil {
		return err
	}
	rec := &recovered{notes: map[string]salvage{}, drafts: map[string]salvage{}}
	if err := v.sweepArtifacts(ctx, rootDir, 0, rec); err != nil {
		return err
	}

	idx, usable, err := v.readIndex(ctx, ignoreIndex)
	if err != nil {
		return err
	}

	entries, err := v.st.list(ctx, vaultDir)
	if err != nil {
		return fmt.Errorf("compendium: list vault: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir || !strings.HasSuffix(e.Name, ".md") {
			continue
		}
		stem := strings.TrimSuffix(e.Name, ".md")
		title := titleFromStem(stem)
		if err := validTitle(title); err != nil || fileStem(title) != stem {
			v.stats.Warnings = append(v.stats.Warnings, fmt.Sprintf("skipped %s: not a Compendium note name", e.Name))
			continue
		}
		if v.notes[fold(title)] != nil {
			v.stats.Warnings = append(v.stats.Warnings, fmt.Sprintf("skipped %s: duplicates %q ignoring case", e.Name, title))
			continue
		}
		if len(v.notes) >= MaxNotes {
			v.stats.warn("vault exceeds %d notes; the rest are not loaded (files left untouched)", MaxNotes)
			break
		}
		if e.Size > MaxNoteBytes {
			v.stats.Skipped++
			v.stats.warn("skipped %s: %d bytes is over the %d-byte note limit (file left untouched)", e.Name, e.Size, MaxNoteBytes)
			continue
		}
		if v.bytes+int(e.Size) > MaxVaultBytes {
			v.stats.warn("vault exceeds %d bytes; the rest are not loaded (files left untouched)", MaxVaultBytes)
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := v.st.read(ctx, vaultDir+"/"+e.Name)
		if err != nil {
			v.stats.Skipped++
			v.stats.warn("skipped %s: %v", e.Name, err)
			continue
		}
		if len(data) > MaxNoteBytes {
			v.stats.Skipped++
			v.stats.warn("skipped %s: %d bytes is over the %d-byte note limit (file left untouched)", e.Name, len(data), MaxNoteBytes)
			continue
		}
		n := &note{title: title, stem: stem, content: string(data), sha: digest(string(data))}
		if ie, ok := idx.Notes[e.Name]; ok && ie.SHA256 == n.sha && ie.Title == title {
			n.links, n.tags = ie.Links, ie.Tags
		} else {
			p := parseNote(n.content)
			n.links, n.tags = p.Links, p.Tags
			v.stats.Reparsed++
		}
		seen[e.Name] = true
		v.addNote(n)
	}
	extra := false
	for name := range idx.Notes {
		extra = extra || !seen[name]
	}
	v.stats.Rebuilt = !usable || v.stats.Reparsed > 0 || extra
	if v.stats.Rebuilt {
		v.persistIndex(ctx)
	}
	if err := v.loadDrafts(ctx, rec); err != nil {
		return err
	}
	return v.loadTrash(ctx)
}

// readIndex returns the persisted index when it is present, within the
// size limit, and well formed. Anything else is reported and discarded:
// the index is a cache and never stands between the user and the notes.
func (v *vault) readIndex(ctx context.Context, ignore bool) (indexFile, bool, error) {
	empty := indexFile{Notes: map[string]indexEntry{}}
	if ignore {
		return empty, false, nil
	}
	entries, err := v.st.list(ctx, rootDir)
	if err != nil {
		return empty, false, err
	}
	for _, e := range entries {
		if e.Name != path.Base(indexPath) || e.IsDir {
			continue
		}
		if e.Size > maxIndexBytes {
			v.stats.warn("discarded index.json: %d bytes is over the %d-byte limit; rebuilt from the notes", e.Size, maxIndexBytes)
			if err := v.st.remove(ctx, indexPath, false); err != nil {
				v.stats.warn("could not remove the oversized index: %v", err)
			}
			return empty, false, nil
		}
		data, err := v.st.read(ctx, indexPath)
		if err != nil {
			v.stats.warn("discarded index.json: %v; rebuilt from the notes", err)
			return empty, false, nil
		}
		var f indexFile
		if json.Unmarshal(data, &f) != nil || f.Version != indexVersion || f.Notes == nil {
			v.stats.warn("discarded unreadable index.json; rebuilt from the notes")
			return empty, false, nil
		}
		return f, true, nil
	}
	return empty, false, nil
}

func (v *vault) ensureLayout(ctx context.Context) error {
	have := map[string]bool{}
	if entries, err := v.st.list(ctx, rootDir); err == nil {
		for _, e := range entries {
			have[e.Name] = e.IsDir
		}
	}
	for _, d := range []string{vaultDir, historyDir, draftsDir, trashDir, exportsDir} {
		if !have[path.Base(d)] {
			if err := v.st.mkdir(ctx, d); err != nil {
				return fmt.Errorf("compendium: prepare storage: %w", err)
			}
		}
	}
	return nil
}

// sweepArtifacts removes staging files left by a save killed mid-write
// (the target is untouched by construction) and collects .recover
// artifacts from a save whose final rename failed. A note's .recover
// becomes a recovery draft; everything else derived is simply discarded.
func (v *vault) sweepArtifacts(ctx context.Context, dir string, depth int, rec *recovered) error {
	if depth > 4 {
		return nil
	}
	entries, err := v.st.list(ctx, dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := dir + "/" + e.Name
		switch {
		case e.IsDir:
			if err := v.sweepArtifacts(ctx, p, depth+1, rec); err != nil {
				return err
			}
		case stagingName.MatchString(e.Name):
			if err := v.st.remove(ctx, p, false); err != nil {
				return err
			}
			v.stats.Recovered++
		case strings.HasSuffix(e.Name, ".recover"):
			// A note's or draft's .recover may be the only copy of an edit:
			// it is removed only after loadDrafts has durably staged its
			// content as a draft. Other artifacts are derived data.
			target := strings.TrimSuffix(e.Name, ".recover")
			switch {
			case dir == vaultDir && strings.HasSuffix(target, ".md"):
				data, err := v.st.read(ctx, p)
				if err != nil {
					v.stats.warn("kept %s: %v", e.Name, err)
					continue
				}
				rec.notes[titleFromStem(strings.TrimSuffix(target, ".md"))] = salvage{content: string(data), artifact: p}
			case dir == draftsDir && strings.HasSuffix(target, ".json"):
				// A newer draft whose final rename failed supersedes the old one.
				data, err := v.st.read(ctx, p)
				if err != nil {
					v.stats.warn("kept %s: %v", e.Name, err)
					continue
				}
				var d draft
				if json.Unmarshal(data, &d) == nil && d.Title != "" {
					rec.drafts[d.Title] = salvage{content: d.Content, artifact: p, base: d.BaseSHA, stagedAt: d.StagedAt}
				} else if err := v.st.remove(ctx, p, false); err != nil {
					return err
				}
			default:
				if err := v.st.remove(ctx, p, false); err != nil {
					return err
				}
				v.stats.Recovered++
			}
		}
	}
	return nil
}

// recovered holds content salvaged from .recover artifacts, by title.
type recovered struct {
	notes  map[string]salvage // a note save whose final rename failed
	drafts map[string]salvage // a draft save whose final rename failed
}

type salvage struct {
	content  string
	artifact string // the .recover file, removed once the content is safe
	// For a draft artifact: what the draft was based on and when it was
	// staged, so it is merged with the draft on disk by recency.
	base     string
	stagedAt time.Time
}

// settle removes a .recover artifact whose content is now held durably
// elsewhere. Failing to remove it is harmless: it is salvaged again.
func (v *vault) settle(ctx context.Context, s salvage) {
	if err := v.st.remove(ctx, s.artifact, false); err != nil {
		v.stats.warn("could not remove %s: %v", path.Base(s.artifact), err)
		return
	}
	v.stats.Recovered++
}

func (v *vault) loadDrafts(ctx context.Context, rec *recovered) error {
	entries, err := v.st.list(ctx, draftsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir || !strings.HasSuffix(e.Name, ".json") {
			continue
		}
		p := draftsDir + "/" + e.Name
		data, err := v.st.read(ctx, p)
		if err != nil {
			return err
		}
		var d draft
		if json.Unmarshal(data, &d) != nil || validTitle(d.Title) != nil {
			// Unreadable, but possibly someone's text: set aside, not deleted.
			dst, err := v.quarantine(ctx, p)
			if err != nil {
				return err
			}
			v.stats.warn("set aside unreadable draft %s as %s", e.Name, dst)
			continue
		}
		if prev := v.drafts[fold(d.Title)]; prev != nil {
			v.stats.warn("two drafts for %q differ only by case; using %s", d.Title, e.Name)
		}
		v.drafts[fold(d.Title)] = &d
	}
	// A draft whose own save failed is usually newer than the draft on
	// disk, but not always: a later save of the same draft may have
	// succeeded (in a process that died before it could remove the
	// artifact). The artifact replaces the staged draft only when it is
	// strictly newer; otherwise both are kept, the artifact under its own
	// title. The artifact is removed only once its content is durable.
	for _, t := range sortedSalvage(rec.drafts) {
		s := rec.drafts[t]
		if validTitle(t) != nil {
			continue
		}
		title := t
		if cur := v.drafts[fold(t)]; cur != nil {
			switch {
			case cur.Content == s.content:
				v.settle(ctx, s)
				continue
			case s.stagedAt.After(cur.StagedAt):
				title = cur.Title
			default:
				title = v.altTitle(t)
			}
		}
		d := &draft{Title: title, Content: s.content, BaseSHA: s.base, StagedAt: s.stagedAt}
		if err := v.writeDraft(ctx, d); err != nil {
			v.stats.warn("kept %s: staging its draft failed: %v", path.Base(s.artifact), err)
			continue
		}
		v.settle(ctx, s)
	}
	// A failed note save becomes a draft unless it is already committed
	// (journal replay) or already held by the user's newer draft. If a
	// different draft exists, the salvage gets its own title: never lost.
	for _, t := range sortedSalvage(rec.notes) {
		s := rec.notes[t]
		content := s.content
		if validTitle(t) != nil {
			continue
		}
		if n := v.notes[fold(t)]; n != nil && n.content == content {
			v.settle(ctx, s)
			continue
		}
		if d := v.drafts[fold(t)]; d != nil {
			if d.Content == content {
				v.settle(ctx, s)
				continue
			}
			t = v.altTitle(t)
		}
		if err := v.stageDraft(ctx, t, content); err != nil {
			v.stats.warn("kept %s: staging its draft failed: %v", path.Base(s.artifact), err)
			continue
		}
		v.settle(ctx, s)
	}
	// A draft identical to what is on disk is an edit that was committed
	// before the process died; it is cleanly removed, not offered.
	for k, d := range v.drafts {
		if n := v.notes[k]; n != nil && n.content == d.Content {
			if err := v.st.remove(ctx, draftsDir+"/"+fileStem(d.Title)+".json", false); err != nil {
				return err
			}
			delete(v.drafts, k)
		}
	}
	return nil
}

// altTitle returns a free title for content recovered beside t's own.
func (v *vault) altTitle(t string) string {
	alt := t + " (recovered)"
	for i := 2; v.notes[fold(alt)] != nil || v.drafts[fold(alt)] != nil || validTitle(alt) != nil || v.normClash(alt, nil) != nil; i++ {
		alt = fmt.Sprintf("%s (recovered %d)", clip(t, MaxTitleBytes-20), i)
	}
	return alt
}

func (v *vault) loadTrash(ctx context.Context) error {
	entries, err := v.st.list(ctx, trashDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir {
			continue
		}
		inner, err := v.st.list(ctx, trashDir+"/"+e.Name)
		if err != nil {
			return err
		}
		for _, f := range inner {
			if !f.IsDir && strings.HasSuffix(f.Name, ".md") {
				stem := strings.TrimSuffix(f.Name, ".md")
				v.trash = append(v.trash, trashEntry{ID: e.Name, Title: titleFromStem(stem), Stem: stem})
				break
			}
		}
	}
	sort.Slice(v.trash, func(i, j int) bool { return v.trash[i].ID < v.trash[j].ID })
	return nil
}

// addNote inserts or replaces n in every in-memory structure.
func (v *vault) addNote(n *note) {
	k := fold(n.title)
	if old := v.notes[k]; old != nil {
		v.dropNote(old)
		n.doc = old.doc
	} else {
		n.doc = v.nextDoc
		v.nextDoc++
	}
	v.notes[k] = n
	v.byDoc[n.doc] = n
	v.norm[normKey(n.title)] = n
	v.bytes += len(n.content)
	for _, l := range n.links {
		set := v.back[fold(l)]
		if set == nil {
			set = map[string]bool{}
			v.back[fold(l)] = set
		}
		set[k] = true
	}
	for _, t := range n.tags {
		set := v.tagIdx[t]
		if set == nil {
			set = map[string]bool{}
			v.tagIdx[t] = set
		}
		set[k] = true
	}
	v.search.add(n.doc, n.title, n.content)
}

func (v *vault) dropNote(n *note) {
	k := fold(n.title)
	for _, l := range n.links {
		if set := v.back[fold(l)]; set != nil {
			delete(set, k)
			if len(set) == 0 {
				delete(v.back, fold(l))
			}
		}
	}
	for _, t := range n.tags {
		if set := v.tagIdx[t]; set != nil {
			delete(set, k)
			if len(set) == 0 {
				delete(v.tagIdx, t)
			}
		}
	}
	v.search.remove(n.doc)
	if nk := normKey(n.title); v.norm[nk] == n {
		delete(v.norm, nk)
	}
	delete(v.notes, k)
	delete(v.byDoc, n.doc)
	v.bytes -= len(n.content)
}

func newNote(title, content string) *note {
	p := parseNote(content)
	return &note{title: title, stem: fileStem(title), content: content, sha: digest(content), links: p.Links, tags: p.Tags}
}

// persistIndex writes the index cache. It never fails the operation that
// called it: the notes are already durable, and an index that cannot be
// written (too large, I/O error) is rebuilt from them on the next open.
func (v *vault) persistIndex(ctx context.Context) {
	f := indexFile{Version: indexVersion, Notes: make(map[string]indexEntry, len(v.notes))}
	for _, n := range v.notes {
		f.Notes[n.stem+".md"] = indexEntry{Title: n.title, Size: len(n.content), SHA256: n.sha, Links: n.links, Tags: n.tags}
	}
	data, err := json.Marshal(f)
	switch {
	case err != nil:
	case len(data) > maxIndexBytes:
		err = fmt.Errorf("the index would be %d bytes, over the %d-byte limit; notes are reparsed at each open instead", len(data), maxIndexBytes)
	default:
		err = v.st.save(ctx, indexPath, data, true)
	}
	if err != nil {
		v.stats.IndexProblem = "index not persisted: " + err.Error()
		return
	}
	v.stats.IndexProblem = ""
}

// replayJournal finishes a multi-file operation interrupted by a crash.
// The journal is written atomically before the first step, so it is
// either absent (nothing happened) or complete (redo every step). A
// journal that cannot be completed never stops the vault from opening:
// the steps that are safe are finished, the journal is set aside for
// inspection, and the problem is reported in the load warnings.
func (v *vault) replayJournal(ctx context.Context) error {
	ok, err := v.st.exists(ctx, journalPath)
	if err != nil || !ok {
		return err
	}
	data, err := v.st.read(ctx, journalPath)
	var j journal
	if err != nil || json.Unmarshal(data, &j) != nil || j.Version != 1 {
		where := v.quarantineJournal(ctx)
		v.stats.warn("set aside an unreadable journal as %s", where)
		return nil
	}
	if err := v.applyOps(ctx, j.Ops); err != nil {
		if j.Kind == "restore" && v.partialRestore(ctx, j.Ops) {
			v.pending = true
			v.stats.warn("an interrupted restore could not be completed (%v); kept its journal for retry", err)
			return nil
		}
		skipped := v.salvageOps(ctx, j.Ops)
		where := v.quarantineJournal(ctx)
		v.stats.warn("an interrupted %s could not be completed (%v); finished the steps that were safe, skipped %d, and kept the journal as %s", j.Kind, err, skipped, where)
		return nil
	}
	v.stats.JournalReplayed = true
	return v.st.remove(ctx, journalPath, false)
}

// A restore whose note already moved must keep its remaining history and
// draft discoverable, and retain the journal until both can follow it.
func (v *vault) partialRestore(ctx context.Context, ops []jop) bool {
	for _, op := range ops {
		if op.Op != "move" || !strings.HasPrefix(op.Src, trashDir+"/") ||
			path.Dir(op.Dst) != vaultDir || !strings.HasSuffix(op.Dst, ".md") {
			continue
		}
		source, err := v.st.exists(ctx, op.Src)
		if err != nil || source {
			continue
		}
		dest, err := v.st.exists(ctx, op.Dst)
		if err != nil || !dest {
			continue
		}
		p := path.Dir(op.Src) + "/draft.json"
		if data, err := v.st.read(ctx, p); err == nil {
			var d draft
			if json.Unmarshal(data, &d) == nil && validTitle(d.Title) == nil {
				v.drafts[fold(d.Title)] = &d
			}
		}
		return true
	}
	return false
}

// quarantineJournal moves the journal aside so it can no longer block
// opening, keeping its data for manual recovery. If even that fails it is
// removed: a journal that can neither run nor move must not brick the vault.
func (v *vault) quarantineJournal(ctx context.Context) string {
	if dst, err := v.quarantine(ctx, journalPath); err == nil {
		return dst
	}
	if err := v.st.remove(ctx, journalPath, false); err != nil {
		return "journal.json (could not be moved or removed: " + err.Error() + ")"
	}
	return "(removed; it could not be moved)"
}

// salvageOps finishes what it safely can of a journal that failed: moves
// whose destination is free, and writes to files that exist and are not
// the destination of a move that could not happen (that file belongs to
// someone else). It never removes anything a failed move left behind: a
// remove step is skipped when any move whose source lies inside it did
// not complete, and a trash entry (trash/<id>/) whose note could not move
// out stays whole, history and draft included, so it remains listed and
// restorable; likewise a note that could not move into the trash keeps its
// history and draft beside it. It returns the number of steps it skipped.
func (v *vault) salvageOps(ctx context.Context, ops []jop) int {
	skipped := 0
	var blocked, stranded, sealed []string
	under := func(p string, roots []string) bool {
		for _, b := range roots {
			if p == b || strings.HasPrefix(p, b+"/") {
				return true
			}
		}
		return false
	}
	within := func(root string, paths []string) bool {
		for _, p := range paths {
			if p == root || strings.HasPrefix(p, root+"/") {
				return true
			}
		}
		return false
	}
	for _, op := range ops {
		switch op.Op {
		case "write":
			ok, err := v.st.exists(ctx, op.Path)
			if err != nil || !ok || under(op.Path, blocked) || v.applyOps(ctx, []jop{op}) != nil {
				skipped++
			}
		case "move":
			if under(op.Src, blocked) || under(op.Src, stranded) || under(op.Dst, sealed) || v.applyOps(ctx, []jop{op}) != nil {
				skipped++
				blocked = append(blocked, op.Dst)
				stranded = append(stranded, op.Src)
				if entry := trashEntryOf(op.Src); entry != "" {
					stranded = append(stranded, entry)
				}
				// A note that could not move into the trash keeps its
				// history and draft with it, out of a half-built entry.
				if entry := trashEntryOf(op.Dst); entry != "" {
					sealed = append(sealed, entry)
				}
			}
		case "remove":
			if under(op.Path, blocked) || within(op.Path, stranded) || v.applyOps(ctx, []jop{op}) != nil {
				skipped++
			}
		default:
			skipped++
		}
	}
	return skipped
}

// trashEntryOf returns the trash entry directory (trash/<id>) containing
// p, or "" when p is not inside the trash.
func trashEntryOf(p string) string {
	rest, ok := strings.CutPrefix(p, trashDir+"/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	return trashDir + "/" + id
}

// errPending is returned while an earlier operation is unresolved.
type errPending struct {
	kind string
	err  error
}

func (e errPending) Error() string {
	return fmt.Sprintf("an earlier %s did not finish and could not be completed yet (%v); nothing else can change until it is. Retry, or reopen Compendium to set it aside", e.kind, e.err)
}

// ready refuses changes while a journaled operation is unresolved. If the
// journal can now be completed, it is, and memory is reloaded from storage
// (the failed operation left memory describing the old state).
func (v *vault) ready(ctx context.Context) error {
	if !v.pending {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	ok, err := v.st.exists(ctx, journalPath)
	if err != nil {
		return errPending{"operation", err}
	}
	if ok {
		data, err := v.st.read(ctx, journalPath)
		if err != nil {
			return errPending{"operation", err}
		}
		var j journal
		if json.Unmarshal(data, &j) != nil || j.Version != 1 {
			return errPending{"operation", fmt.Errorf("its journal is unreadable")}
		}
		if err := v.applyOps(ctx, j.Ops); err != nil {
			return errPending{j.Kind, err}
		}
		if err := v.st.remove(ctx, journalPath, false); err != nil {
			return errPending{j.Kind, err}
		}
	}
	return v.load(ctx, false)
}

// runJournal makes ops all-or-nothing across a crash: record, apply, clear.
// An ordinary failure after the journal is recorded leaves it pending; it
// is completed by ready() before anything else changes, never overwritten.
func (v *vault) runJournal(ctx context.Context, kind string, ops []jop) error {
	total := 0
	for _, op := range ops {
		total += len(op.Data)
	}
	if total > maxJournalData {
		return fmt.Errorf("%s would rewrite %d bytes; the limit for one operation is %d", kind, total, maxJournalData)
	}
	data, err := json.Marshal(journal{Version: 1, Kind: kind, Ops: ops})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err // canceled before anything committed
	}
	if v.pending {
		return errPending{"operation", fmt.Errorf("a journal is still pending")}
	}
	if ok, err := v.st.exists(ctx, journalPath); err != nil {
		return err
	} else if ok {
		v.pending = true // someone else's journal: finish it first
		return errPending{"operation", fmt.Errorf("a journal is still pending")}
	}
	if err := v.st.save(ctx, journalPath, data, true); err != nil {
		v.pending = true // the save may have landed even though it failed
		return err
	}
	ctx = context.WithoutCancel(ctx)
	if err := v.applyOps(ctx, ops); err != nil {
		v.pending = true
		return fmt.Errorf("%s did not finish (%w); it will be completed before the next change", kind, err)
	}
	if err := v.st.remove(ctx, journalPath, false); err != nil {
		v.pending = true
		return err
	}
	return nil
}

// caseOnly reports whether src and dst may name the same entry on a case-
// or normalization-insensitive host (they differ only by case or by
// Unicode normalization), so the move has to go through a temporary name.
func caseOnly(src, dst string) bool { return src != dst && normKey(src) == normKey(dst) }

// caseTemp is the deterministic intermediate name for a case-only move.
func caseTemp(dst string) string {
	return path.Dir(dst) + "/.casefold-" + digest(dst)[:16]
}

// applyOps executes journal steps; each is idempotent so replay is safe.
func (v *vault) applyOps(ctx context.Context, ops []jop) error {
	for _, op := range ops {
		switch op.Op {
		case "move":
			if caseOnly(op.Src, op.Dst) {
				if err := v.moveCase(ctx, op.Src, op.Dst); err != nil {
					return err
				}
				continue
			}
			srcOK, err := v.st.exists(ctx, op.Src)
			if err != nil {
				return err
			}
			if !srcOK {
				continue // already moved, or nothing to move
			}
			dstOK, err := v.st.exists(ctx, op.Dst)
			if err != nil {
				return err
			}
			if dstOK {
				return fmt.Errorf("move %s: destination %s exists", op.Src, op.Dst)
			}
			if err := v.st.mkdir(ctx, path.Dir(op.Dst)); err != nil {
				return err
			}
			if err := v.st.rename(ctx, op.Src, op.Dst); err != nil {
				return err
			}
		case "write":
			if err := v.st.save(ctx, op.Path, op.Data, true); err != nil {
				return err
			}
		case "remove":
			ok, err := v.st.exists(ctx, op.Path)
			if err != nil {
				return err
			}
			if ok {
				if err := v.st.remove(ctx, op.Path, true); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unknown journal op %q", op.Op)
		}
	}
	return nil
}

// moveCase renames src to a name differing only in case. A case-insensitive
// host sees the destination as already existing (it is the source), so the
// move goes through a temporary name. exists() compares names exactly, so
// each state is recognizable on replay: src present (start), temp present
// (half done), neither (done).
func (v *vault) moveCase(ctx context.Context, src, dst string) error {
	tmp := caseTemp(dst)
	srcOK, err := v.st.exists(ctx, src)
	if err != nil {
		return err
	}
	tmpOK, err := v.st.exists(ctx, tmp)
	if err != nil {
		return err
	}
	switch {
	case srcOK && tmpOK:
		return fmt.Errorf("move %s: stale temporary %s exists", src, tmp)
	case srcOK:
		if err := v.st.rename(ctx, src, tmp); err != nil {
			return err
		}
	case !tmpOK:
		return nil // done, or nothing to move
	}
	if err := v.st.rename(ctx, tmp, dst); err != nil {
		if back := v.st.rename(ctx, tmp, src); back != nil {
			return fmt.Errorf("move %s: %w (and restoring it failed: %v)", src, err, back)
		}
		return err
	}
	return nil
}

// quarantine moves p under quarantine/ with a unique, sortable name so it
// stops affecting the vault but is never destroyed.
func (v *vault) quarantine(ctx context.Context, p string) (string, error) {
	dir := rootDir + "/quarantine"
	if err := v.st.mkdir(ctx, dir); err != nil {
		return "", err
	}
	entries, err := v.st.list(ctx, dir)
	if err != nil {
		return "", err
	}
	var ids []string
	for _, e := range entries {
		id, _, _ := strings.Cut(e.Name, "-")
		ids = append(ids, id)
	}
	dst := dir + "/" + v.nextSeq(ids) + "-" + path.Base(p)
	return dst, v.st.rename(ctx, p, dst)
}

// existsFold reports the name of an entry in dir equal to name ignoring
// case and Unicode normalization (as APFS or NTFS would see it), or "" if
// there is none.
func (v *vault) existsFold(ctx context.Context, dir, name string) (string, error) {
	ok, err := v.st.exists(ctx, dir)
	if err != nil || !ok {
		return "", err
	}
	entries, err := v.st.list(ctx, dir)
	if err != nil {
		return "", err
	}
	key := normKey(name)
	for _, e := range entries {
		if normKey(e.Name) == key {
			return e.Name, nil
		}
	}
	return "", nil
}

// --- note operations -----------------------------------------------------

func (v *vault) get(title string) (*note, error) {
	n := v.notes[fold(strings.TrimSpace(title))]
	if n == nil {
		return nil, fmt.Errorf("no note titled %q", title)
	}
	return n, nil
}

func (v *vault) create(ctx context.Context, title, content string) (*note, error) {
	if err := v.ready(ctx); err != nil {
		return nil, err
	}
	if err := validTitle(title); err != nil {
		return nil, err
	}
	if len(content) > MaxNoteBytes {
		return nil, fmt.Errorf("note exceeds %d bytes", MaxNoteBytes)
	}
	if v.notes[fold(title)] != nil {
		return nil, fmt.Errorf("a note titled %q already exists", v.notes[fold(title)].title)
	}
	if err := v.normClash(title, nil); err != nil {
		return nil, err
	}
	if len(v.notes) >= MaxNotes {
		return nil, fmt.Errorf("vault is full (%d notes)", MaxNotes)
	}
	if v.bytes+len(content) > MaxVaultBytes {
		return nil, fmt.Errorf("vault is full (%d bytes)", MaxVaultBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := newNote(title, content)
	if err := v.st.save(ctx, n.file(), []byte(content), false); err != nil {
		return nil, err
	}
	v.addNote(n)
	v.persistIndex(context.WithoutCancel(ctx))
	return n, nil
}

// save commits content: snapshot the previous version into history, then
// atomically replace the note, then the index, then drop the draft. A kill
// between any two steps leaves either the old or the new committed note,
// with the draft still present until the new content is durable.
func (v *vault) save(ctx context.Context, title, content string) (*note, error) {
	if err := v.ready(ctx); err != nil {
		return nil, err
	}
	cur := v.notes[fold(title)]
	if cur == nil {
		n, err := v.create(ctx, title, content)
		if err == nil {
			err = v.clearDraft(context.WithoutCancel(ctx), n.title)
		}
		return n, err
	}
	if len(content) > MaxNoteBytes {
		return nil, fmt.Errorf("note exceeds %d bytes", MaxNoteBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := v.checkUnchanged(ctx, cur, content); err != nil {
		return nil, err
	}
	if content == cur.content {
		return cur, v.clearDraft(ctx, cur.title)
	}
	if v.bytes-len(cur.content)+len(content) > MaxVaultBytes {
		return nil, fmt.Errorf("vault is full (%d bytes)", MaxVaultBytes)
	}
	versions, err := v.historyIDs(ctx, historyDir+"/"+cur.stem)
	if err != nil {
		return nil, err
	}
	if cur.content != "" {
		id := v.nextSeq(versions)
		if err := v.st.save(ctx, historyDir+"/"+cur.stem+"/"+id+".md", []byte(cur.content), false); err != nil {
			return nil, err
		}
		versions = append(versions, id)
	}
	ctx = context.WithoutCancel(ctx) // past this point the save commits
	n := newNote(cur.title, content)
	if err := v.st.save(ctx, n.file(), []byte(content), true); err != nil {
		return nil, err
	}
	v.addNote(n)
	v.persistIndex(ctx)
	if err := v.clearDraft(ctx, n.title); err != nil {
		return n, err
	}
	for len(versions) > MaxHistory {
		if err := v.st.remove(ctx, historyDir+"/"+cur.stem+"/"+versions[0]+".md", false); err != nil {
			return n, err
		}
		versions = versions[1:]
	}
	return n, nil
}

// errExternal reports notes whose files changed outside Compendium.
type errExternal struct{ titles []string }

func (e errExternal) Error() string {
	return fmt.Sprintf("%s changed outside Compendium since it was loaded; Compendium now shows the version on disk and wrote nothing over it", quoteList(e.titles))
}

func quoteList(titles []string) string {
	q := make([]string, len(titles))
	for i, t := range titles {
		q[i] = strconv.Quote(t)
	}
	if len(q) > 5 {
		q = append(q[:5], fmt.Sprintf("and %d more", len(q)-5))
	}
	return strings.Join(q, ", ")
}

// onDisk reports whether n's file still holds exactly what memory holds.
// When it does not, memory is refreshed from the file (so the user sees
// the external version and a retry compares against it) and the file is
// left untouched. A file that can no longer be read is reported as
// changed, never overwritten blind.
func (v *vault) onDisk(ctx context.Context, n *note) (bool, error) {
	data, err := v.st.read(ctx, n.file())
	if err != nil {
		if ok, xerr := v.st.exists(ctx, n.file()); xerr == nil && !ok {
			v.dropNote(n)
			v.persistIndex(ctx)
			return false, nil // removed outside Compendium
		}
		return false, err
	}
	if digest(string(data)) == n.sha {
		return true, nil
	}
	if len(data) <= MaxNoteBytes && v.bytes-len(n.content)+len(data) <= MaxVaultBytes {
		v.addNote(newNote(n.title, string(data)))
		v.persistIndex(ctx)
	}
	return false, nil
}

// checkUnchanged is the external-edit guard for save: when the note's file
// changed outside Compendium, nothing is written over it; the content
// being saved is kept as a draft on top of the external version, and the
// caller gets a conflict naming the note.
func (v *vault) checkUnchanged(ctx context.Context, cur *note, content string) error {
	same, err := v.onDisk(ctx, cur)
	if err != nil {
		return err
	}
	if same {
		return nil
	}
	conflict := errExternal{[]string{cur.title}}
	if err := v.stageDraft(context.WithoutCancel(ctx), cur.title, content); err != nil {
		return fmt.Errorf("%v; keeping your edit as a draft also failed: %w", conflict, err)
	}
	return fmt.Errorf("%w; your edit is kept as a draft (restore it to save it over the external version, which then goes to history)", conflict)
}

// historyIDs lists a history directory's version IDs, oldest first.
func (v *vault) historyIDs(ctx context.Context, dir string) ([]string, error) {
	ok, err := v.st.exists(ctx, dir)
	if err != nil || !ok {
		return nil, err
	}
	entries, err := v.st.list(ctx, dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir && strings.HasSuffix(e.Name, ".md") {
			ids = append(ids, strings.TrimSuffix(e.Name, ".md"))
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// nextSeq returns a sortable, unique ID: the clock in nanoseconds, bumped
// past any existing ID so a coarse or repeated clock cannot collide.
func (v *vault) nextSeq(existing []string) string {
	next := v.now().UnixNano()
	for _, id := range existing {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil && n >= next {
			next = n + 1
		}
	}
	return fmt.Sprintf("%020d", next)
}

func (v *vault) stageDraft(ctx context.Context, title, content string) error {
	return v.stageDraftOn(ctx, title, content, nil)
}

// stageDraftOn stages an edit made on top of the version whose hash is
// *base (nil: the current note), so a later save can detect that the note
// changed underneath the edit.
func (v *vault) stageDraftOn(ctx context.Context, title, content string, base *string) error {
	if err := v.ready(ctx); err != nil {
		return err
	}
	if len(content) > MaxNoteBytes {
		return fmt.Errorf("draft exceeds %d bytes", MaxNoteBytes)
	}
	baseSHA := ""
	if n := v.notes[fold(title)]; n != nil {
		if n.content == content {
			return v.clearDraft(ctx, n.title)
		}
		title, baseSHA = n.title, n.sha
	} else if old := v.drafts[fold(title)]; old != nil {
		// Same draft under another capitalization: keep its file name, so
		// no second file is left behind to compete on the next open (and a
		// case-insensitive host never sees two names for one file).
		title = old.Title
	} else if err := v.normClash(title, nil); err != nil {
		return err
	}
	if base != nil {
		baseSHA = *base
	}
	return v.writeDraft(ctx, &draft{Title: title, Content: content, BaseSHA: baseSHA, StagedAt: v.now().UTC()})
}

// writeDraft durably stages d under its own file. A draft file memory does
// not know yet is created with overwrite=false, so the host itself refuses
// a name it already resolves to another file (a case or normalization
// alias, or a draft written by someone else since the vault was loaded).
// After a successful write, a .recover artifact left by an earlier failed
// save of the same file in this process is superseded and removed.
func (v *vault) writeDraft(ctx context.Context, d *draft) error {
	if len(d.Content) > MaxNoteBytes {
		return fmt.Errorf("draft exceeds %d bytes", MaxNoteBytes)
	}
	data, err := marshalDraft(d)
	if err != nil {
		return err
	}
	p := draftsDir + "/" + fileStem(d.Title) + ".json"
	known := v.drafts[fold(d.Title)] != nil
	if err := v.st.save(ctx, p, data, known); err != nil {
		v.failedSaves[p] = true
		if !known {
			if ok, xerr := v.st.exists(ctx, draftsDir); xerr == nil && ok {
				if got, _ := v.existsFold(ctx, draftsDir, path.Base(p)); got != "" {
					return fmt.Errorf("a draft file %q already exists that this host treats as the same name as the draft for %q; it was left untouched (reopen Compendium to load it): %w", got, d.Title, err)
				}
			}
		}
		return err
	}
	v.drafts[fold(d.Title)] = d
	if v.failedSaves[p] {
		delete(v.failedSaves, p)
		v.dropStaleArtifact(ctx, p+".recover")
	}
	return nil
}

// marshalDraft encodes a draft without HTML escaping: encoding/json would
// otherwise write each <, > and & as six bytes, and a valid 1 MiB note of
// markup would encode past the IPC write frame.
func marshalDraft(d *draft) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// dropStaleArtifact removes a .recover artifact whose content a later
// successful save of the same file has superseded. Failing to remove it
// is harmless: on the next open it is merged by recency, never over the
// newer draft.
func (v *vault) dropStaleArtifact(ctx context.Context, p string) {
	ok, err := v.st.exists(ctx, p)
	if err != nil || !ok {
		return
	}
	if err := v.st.remove(ctx, p, false); err != nil {
		v.stats.warn("could not remove the superseded %s: %v", path.Base(p), err)
	}
}

// normClash rejects a title that differs from every existing note and
// draft by fold (so the in-memory maps would keep it apart) but would
// share a file name with one of them on a host that ignores Unicode
// normalization, as macOS APFS does: "Caf\u00e9" and "Cafe\u0301". own is
// the note being renamed (its own entries do not count).
func (v *vault) normClash(title string, own *note) error {
	k, f := normKey(title), fold(title)
	if m := v.norm[k]; m != nil && m != own && fold(m.title) != f {
		return fmt.Errorf("the title %q is the note %q written with different Unicode composition; macOS and other hosts store both under one file name, so it cannot be used for a separate note", title, m.title)
	}
	for _, d := range v.drafts {
		if fold(d.Title) != f && normKey(d.Title) == k && (own == nil || fold(d.Title) != fold(own.title)) {
			return fmt.Errorf("the title %q is the draft %q written with different Unicode composition; macOS and other hosts store both under one file name, so it cannot be used for a separate note", title, d.Title)
		}
	}
	return nil
}

// clearDraft removes title's draft. ready() may reload memory (finishing
// an interrupted operation that moved or removed the draft), so the draft
// is looked up only after it.
func (v *vault) clearDraft(ctx context.Context, title string) error {
	if err := v.ready(ctx); err != nil {
		return err
	}
	d := v.drafts[fold(title)]
	if d == nil {
		return nil
	}
	if err := v.st.remove(ctx, draftsDir+"/"+fileStem(d.Title)+".json", false); err != nil {
		return err
	}
	delete(v.drafts, fold(title))
	return nil
}

type orphan struct {
	Note     string `json:"note"`
	Location string `json:"location"`
	Target   string `json:"target"`
}

type renameResult struct {
	From           string   `json:"from"`
	To             string   `json:"to"`
	RewrittenNotes []string `json:"rewritten_notes"`
	RewrittenLinks int      `json:"rewritten_links"`
	Orphaned       []orphan `json:"orphaned"`
}

// rename moves a note and rewrites every [[link]] to it, in live notes and
// pending drafts, as one journaled operation. Links that cannot be
// rewritten (inside trashed notes) are reported as orphaned.
func (v *vault) rename(ctx context.Context, oldTitle, newTitle string) (renameResult, error) {
	res := renameResult{RewrittenNotes: []string{}, Orphaned: []orphan{}}
	if err := v.ready(ctx); err != nil {
		return res, err
	}
	// Backlinks are derived from every source, not just notes already known
	// to link here. Refresh all loaded notes before selecting rewrites.
	var external []string
	for _, m := range v.sortedNotes() {
		same, err := v.onDisk(ctx, m)
		if err != nil {
			return res, err
		}
		if !same {
			external = append(external, m.title)
		}
	}
	if len(external) > 0 {
		return res, fmt.Errorf("rename not started: %w; retry to rewrite current links", errExternal{external})
	}
	n, err := v.get(oldTitle)
	if err != nil {
		return res, err
	}
	if err := validTitle(newTitle); err != nil {
		return res, err
	}
	if other := v.notes[fold(newTitle)]; other != nil && other != n {
		return res, fmt.Errorf("a note titled %q already exists", other.title)
	}
	if err := v.normClash(newTitle, n); err != nil {
		return res, err
	}
	res.From, res.To = n.title, newTitle
	if newTitle == n.title {
		return res, nil
	}
	newStem := fileStem(newTitle)
	if err := v.checkFree(ctx, newTitle, n.stem, n.title); err != nil {
		return res, err
	}
	ops := []jop{{Op: "move", Src: n.file(), Dst: vaultDir + "/" + newStem + ".md"}}
	ops = append(ops, jop{Op: "move", Src: historyDir + "/" + n.stem, Dst: historyDir + "/" + newStem})

	type update struct {
		n       *note
		content string
	}
	var updates []update
	keys := make([]string, 0, len(v.notes))
	for k := range v.notes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m := v.notes[k]
		if !v.back[fold(n.title)][k] {
			continue
		}
		content, count := rewriteLinks(m.content, n.title, newTitle)
		if count == 0 {
			continue
		}
		file := m.file()
		title := m.title
		if m == n {
			file, title = vaultDir+"/"+newStem+".md", newTitle
		}
		ops = append(ops, jop{Op: "write", Path: file, Data: []byte(content)})
		updates = append(updates, update{m, content})
		res.RewrittenNotes = append(res.RewrittenNotes, title)
		res.RewrittenLinks += count
	}
	// Pending drafts follow the rename too, so recovering one later does
	// not resurrect a dangling link or a draft for a title that is gone.
	newDrafts := map[string]*draft{}
	draftKeys := make([]string, 0, len(v.drafts))
	for k := range v.drafts {
		draftKeys = append(draftKeys, k)
	}
	sort.Strings(draftKeys)
	var oversized []string
	growth := 0
	for _, u := range updates {
		growth += len(u.content) - len(u.n.content)
		if len(u.content) > MaxNoteBytes {
			oversized = append(oversized, u.n.title)
		}
	}
	for _, k := range draftKeys {
		d := v.drafts[k]
		content, _ := rewriteLinks(d.Content, n.title, newTitle)
		if len(content) > MaxNoteBytes {
			oversized = append(oversized, d.Title+" (draft)")
		}
	}
	// Every derived write is preflighted against the limits a later open
	// enforces, so a rename can never make a note unloadable.
	if len(oversized) > 0 {
		return res, fmt.Errorf("renaming %q to %q would grow %s past the %d-byte note limit; nothing was changed. Shorten the new title or those notes first", n.title, newTitle, quoteList(oversized), MaxNoteBytes)
	}
	if v.bytes+growth > MaxVaultBytes {
		return res, fmt.Errorf("renaming %q to %q would grow the vault past its %d-byte limit; nothing was changed", n.title, newTitle, MaxVaultBytes)
	}
	// A rewrite replaces whole files: never over one changed outside
	// Compendium. Those notes are reloaded from disk and the rename is
	// refused, so a retry rewrites the current text.
	var changed []string
	check := []*note{n}
	for _, u := range updates {
		if u.n != n {
			check = append(check, u.n)
		}
	}
	for _, m := range check {
		same, err := v.onDisk(ctx, m)
		if err != nil {
			return res, err
		}
		if !same {
			changed = append(changed, m.title)
		}
	}
	if len(changed) > 0 {
		return res, fmt.Errorf("rename not started: %w; retry the rename to update their links", errExternal{changed})
	}
	for _, k := range draftKeys {
		d := v.drafts[k]
		content, _ := rewriteLinks(d.Content, n.title, newTitle)
		title := d.Title
		file := draftsDir + "/" + fileStem(d.Title) + ".json"
		if k == fold(n.title) {
			title = newTitle
			dst := draftsDir + "/" + newStem + ".json"
			ops = append(ops, jop{Op: "move", Src: file, Dst: dst})
			file = dst
		} else if content == d.Content {
			continue
		}
		nd := *d
		nd.Title, nd.Content = title, content
		data, err := marshalDraft(&nd)
		if err != nil {
			return res, err
		}
		ops = append(ops, jop{Op: "write", Path: file, Data: data})
		newDrafts[k] = &nd
	}
	for _, t := range v.trash {
		data, err := v.st.read(ctx, trashDir+"/"+t.ID+"/"+t.Stem+".md")
		if err != nil {
			return res, err
		}
		if linksTo(string(data), n.title) {
			res.Orphaned = append(res.Orphaned, orphan{Note: t.Title, Location: "trash", Target: n.title})
		}
	}
	if err := v.runJournal(ctx, "rename", ops); err != nil {
		return res, err
	}

	v.dropNote(n)
	moved := newNote(newTitle, n.content)
	for _, u := range updates {
		if u.n == n {
			moved = newNote(newTitle, u.content)
			continue
		}
		v.addNote(newNote(u.n.title, u.content))
	}
	v.addNote(moved)
	for k, d := range newDrafts {
		delete(v.drafts, k)
		v.drafts[fold(d.Title)] = d
	}
	v.persistIndex(context.WithoutCancel(ctx))
	return res, nil
}

// checkFree rejects, before anything is journaled, a destination title
// whose note, history, or draft name is already taken, comparing names
// ignoring case as a case-insensitive host would. The entries of the note
// being renamed (ownStem/ownTitle, empty for a restore) do not count, so a
// capitalization-only rename is allowed.
func (v *vault) checkFree(ctx context.Context, title, ownStem, ownTitle string) error {
	if ownTitle == "" || fold(title) != fold(ownTitle) {
		if d := v.drafts[fold(title)]; d != nil {
			return fmt.Errorf("an unsaved draft titled %q exists; restore or discard it first", d.Title)
		}
	}
	stem := fileStem(title)
	for _, c := range []struct{ dir, name, own, what string }{
		{vaultDir, stem + ".md", ownStem + ".md", "a file"},
		{historyDir, stem, ownStem, "stale history"},
		{draftsDir, stem + ".json", ownStem + ".json", "a draft file"},
	} {
		got, err := v.existsFold(ctx, c.dir, c.name)
		if err != nil {
			return err
		}
		if got != "" && (ownStem == "" || got != c.own) {
			return fmt.Errorf("%s named %q already exists in %s; move it aside before using the title %q", c.what, got, path.Base(c.dir), title)
		}
	}
	return nil
}

// trashNote moves the note, its history, and any draft under one trash ID.
func (v *vault) trashNote(ctx context.Context, title string) (string, error) {
	if err := v.ready(ctx); err != nil {
		return "", err
	}
	n, err := v.get(title)
	if err != nil {
		return "", err
	}
	ids := make([]string, 0, len(v.trash))
	for _, t := range v.trash {
		ids = append(ids, t.ID)
	}
	id := v.nextSeq(ids)
	dir := trashDir + "/" + id
	ops := []jop{
		{Op: "move", Src: n.file(), Dst: dir + "/" + n.stem + ".md"},
		{Op: "move", Src: historyDir + "/" + n.stem, Dst: dir + "/history"},
		{Op: "move", Src: draftsDir + "/" + n.stem + ".json", Dst: dir + "/draft.json"},
	}
	if err := v.runJournal(ctx, "trash", ops); err != nil {
		return "", err
	}
	v.dropNote(n)
	delete(v.drafts, fold(n.title))
	v.trash = append(v.trash, trashEntry{ID: id, Title: n.title, Stem: n.stem})
	v.persistIndex(context.WithoutCancel(ctx))
	return id, nil
}

// restoreNote puts a trashed note, its history, and its draft back.
func (v *vault) restoreNote(ctx context.Context, id string) (*note, error) {
	if err := v.ready(ctx); err != nil {
		return nil, err
	}
	pos := -1
	for i, t := range v.trash {
		if t.ID == id {
			pos = i
		}
	}
	if pos < 0 {
		return nil, fmt.Errorf("no trash entry %q", id)
	}
	t := v.trash[pos]
	if other := v.notes[fold(t.Title)]; other != nil {
		return nil, fmt.Errorf("a note titled %q already exists; rename it before restoring", other.title)
	}
	if err := v.normClash(t.Title, nil); err != nil {
		return nil, err
	}
	if err := v.checkFree(ctx, t.Title, "", ""); err != nil {
		return nil, err
	}
	dir := trashDir + "/" + t.ID
	// Restoring may not push the vault past the limits a later open
	// enforces (it would then skip notes), so check them first.
	if len(v.notes) >= MaxNotes {
		return nil, fmt.Errorf("vault is full (%d notes); the note stays in the trash", MaxNotes)
	}
	entries, err := v.st.list(ctx, dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Name != t.Stem+".md" {
			continue
		}
		if e.Size > MaxNoteBytes {
			return nil, fmt.Errorf("the trashed note is %d bytes, over the %d-byte note limit; it stays in the trash", e.Size, MaxNoteBytes)
		}
		if v.bytes+int(e.Size) > MaxVaultBytes {
			return nil, fmt.Errorf("restoring %q would grow the vault past its %d-byte limit; it stays in the trash", t.Title, MaxVaultBytes)
		}
	}
	ops := []jop{
		{Op: "move", Src: dir + "/" + t.Stem + ".md", Dst: vaultDir + "/" + t.Stem + ".md"},
		{Op: "move", Src: dir + "/history", Dst: historyDir + "/" + t.Stem},
		{Op: "move", Src: dir + "/draft.json", Dst: draftsDir + "/" + t.Stem + ".json"},
		{Op: "remove", Path: dir},
	}
	if err := v.runJournal(ctx, "restore", ops); err != nil {
		return nil, err
	}
	ctx = context.WithoutCancel(ctx)
	v.trash = append(v.trash[:pos:pos], v.trash[pos+1:]...)
	data, err := v.st.read(ctx, vaultDir+"/"+t.Stem+".md")
	if err != nil {
		return nil, err
	}
	n := newNote(t.Title, string(data))
	v.addNote(n)
	if ok, err := v.st.exists(ctx, draftsDir+"/"+t.Stem+".json"); err == nil && ok {
		if raw, err := v.st.read(ctx, draftsDir+"/"+t.Stem+".json"); err == nil {
			var d draft
			if json.Unmarshal(raw, &d) == nil {
				v.drafts[fold(d.Title)] = &d
			}
		}
	}
	v.persistIndex(ctx)
	return n, nil
}

// --- read models -----------------------------------------------------------

func (v *vault) resolve(name string) (string, bool) {
	if n := v.notes[fold(name)]; n != nil {
		return n.title, true
	}
	return "", false
}

type linkInfo struct {
	Target   string `json:"target"`
	Title    string `json:"title,omitempty"`
	Resolved bool   `json:"resolved"`
}

type backlinkInfo struct {
	Title   string `json:"title"`
	Context string `json:"context"`
}

func (v *vault) linksOf(content string) []linkInfo {
	out := []linkInfo{}
	for _, l := range parseNote(content).Links {
		t, ok := v.resolve(l)
		out = append(out, linkInfo{Target: l, Title: t, Resolved: ok})
	}
	return out
}

func (v *vault) backlinks(title string) []backlinkInfo {
	out := []backlinkInfo{}
	for k := range v.back[fold(title)] {
		if src := v.notes[k]; src != nil {
			out = append(out, backlinkInfo{Title: src.title, Context: contextLine(src.content, title)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return fold(out[i].Title) < fold(out[j].Title) })
	return out
}

func (v *vault) sortedNotes() []*note {
	out := make([]*note, 0, len(v.notes))
	for _, n := range v.notes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return fold(out[i].title) < fold(out[j].title) })
	return out
}

type tagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

func (v *vault) tags() []tagCount {
	out := make([]tagCount, 0, len(v.tagIdx))
	for t, set := range v.tagIdx {
		out = append(out, tagCount{t, len(set)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

func (v *vault) tagged(tag string) []string {
	tag = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(tag), "#"))
	out := []string{}
	for k := range v.tagIdx[tag] {
		out = append(out, v.notes[k].title)
	}
	sort.Slice(out, func(i, j int) bool { return fold(out[i]) < fold(out[j]) })
	return out
}

type searchResult struct {
	Title   string  `json:"title"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
}

func (v *vault) find(query string, limit int) ([]searchResult, int) {
	terms := queryTerms(query)
	hits := v.search.query(terms)
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return fold(v.byDoc[hits[i].doc].title) < fold(v.byDoc[hits[j].doc].title)
	})
	total := len(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]searchResult, 0, len(hits))
	for _, h := range hits {
		n := v.byDoc[h.doc]
		out = append(out, searchResult{Title: n.title, Score: float64(int64(h.score*1000+0.5)) / 1000, Snippet: snippet(n.content, terms)})
	}
	return out, total
}

type graphNode struct {
	Title string   `json:"title"`
	In    int      `json:"in"`
	Out   int      `json:"out"`
	Tags  []string `json:"tags"`
}

type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type unresolvedLink struct {
	Target string   `json:"target"`
	From   []string `json:"from"`
}

type graphData struct {
	Nodes      []graphNode      `json:"nodes"`
	Edges      []graphEdge      `json:"edges"`
	DeadEnds   []string         `json:"dead_ends"`
	Orphans    []string         `json:"orphans"`
	Unresolved []unresolvedLink `json:"unresolved"`
}

// graph derives the note graph. A dead end is reachable but links nowhere
// (in > 0, out == 0); an orphan has no resolved links in either direction.
// Unresolved targets are listed with their sources, never dropped.
func (v *vault) graph() graphData {
	g := graphData{Nodes: []graphNode{}, Edges: []graphEdge{}, DeadEnds: []string{}, Orphans: []string{}, Unresolved: []unresolvedLink{}}
	in := map[string]int{}
	out := map[string]int{}
	missing := map[string]*unresolvedLink{}
	for _, n := range v.sortedNotes() {
		for _, l := range n.links {
			if t, ok := v.resolve(l); ok {
				g.Edges = append(g.Edges, graphEdge{From: n.title, To: t})
				out[fold(n.title)]++
				in[fold(t)]++
				continue
			}
			u := missing[fold(l)]
			if u == nil {
				u = &unresolvedLink{Target: l}
				missing[fold(l)] = u
			}
			u.From = append(u.From, n.title)
		}
	}
	for _, n := range v.sortedNotes() {
		k := fold(n.title)
		tags := n.tags
		if tags == nil {
			tags = []string{}
		}
		g.Nodes = append(g.Nodes, graphNode{Title: n.title, In: in[k], Out: out[k], Tags: tags})
		switch {
		case in[k] > 0 && out[k] == 0:
			g.DeadEnds = append(g.DeadEnds, n.title)
		case in[k] == 0 && out[k] == 0:
			g.Orphans = append(g.Orphans, n.title)
		}
	}
	for _, u := range missing {
		g.Unresolved = append(g.Unresolved, *u)
	}
	sort.Slice(g.Unresolved, func(i, j int) bool { return fold(g.Unresolved[i].Target) < fold(g.Unresolved[j].Target) })
	return g
}

// dailyTitle is the title of the daily note for t.
func dailyTitle(t time.Time) string { return t.Format("2006-01-02") }

func sortedSalvage(m map[string]salvage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
