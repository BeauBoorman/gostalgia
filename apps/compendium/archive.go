package compendium

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
)

type exportResult struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Dest       string `json:"dest,omitempty"`
	DataBase64 []byte `json:"data_base64"` // encoding/json emits standard base64
	Files      int    `json:"files"`
	Size       int    `json:"size"`
}

// exportNote writes the note's exact bytes to exports/<stem>.md (and to
// dest, when the caller has granted Compendium access to it).
func (v *vault) exportNote(ctx context.Context, title, dest string) (exportResult, error) {
	n, err := v.get(title)
	if err != nil {
		return exportResult{}, err
	}
	data := []byte(n.content)
	if len(data) > maxExportBytes {
		return exportResult{}, fmt.Errorf("note is %d bytes; single-note export is limited to %d", len(data), maxExportBytes)
	}
	res := exportResult{Name: n.stem + ".md", Path: exportsDir + "/" + n.stem + ".md", DataBase64: data, Files: 1, Size: len(data)}
	return res, v.writeExport(ctx, res, dest)
}

// exportVault zips every live note as <stem>.md, bytes unchanged. An
// archive is written only if Compendium's own import would accept it: at
// most maxImportEntries notes inflating to at most maxImportBytes. So
// anything exported imports again, byte for byte.
func (v *vault) exportVault(ctx context.Context, dest string) (exportResult, error) {
	if len(v.notes) > maxImportEntries || v.bytes > maxImportBytes {
		return exportResult{}, fmt.Errorf("the vault holds %d bytes in %d notes; one archive is limited to %d bytes (what a single import accepts), so export notes individually", v.bytes, len(v.notes), maxImportBytes)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	stamp := v.now()
	notes := v.sortedNotes()
	for _, n := range notes {
		if err := ctx.Err(); err != nil {
			return exportResult{}, err
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: n.stem + ".md", Method: zip.Deflate, Modified: stamp})
		if err != nil {
			return exportResult{}, err
		}
		if _, err := w.Write([]byte(n.content)); err != nil {
			return exportResult{}, err
		}
		if buf.Len() > maxExportBytes {
			return exportResult{}, fmt.Errorf("vault export exceeds %d bytes; export notes individually", maxExportBytes)
		}
	}
	if err := zw.Close(); err != nil {
		return exportResult{}, err
	}
	if buf.Len() > maxExportBytes {
		return exportResult{}, fmt.Errorf("vault export exceeds %d bytes; export notes individually", maxExportBytes)
	}
	name := "compendium-" + stamp.UTC().Format("20060102-150405") + ".zip"
	res := exportResult{Name: name, Path: exportsDir + "/" + name, DataBase64: buf.Bytes(), Files: len(notes), Size: buf.Len()}
	return res, v.writeExport(ctx, res, dest)
}

func (v *vault) writeExport(ctx context.Context, res exportResult, dest string) error {
	if err := v.st.save(ctx, res.Path, res.DataBase64, true); err != nil {
		return err
	}
	if dest != "" {
		if err := v.st.save(ctx, dest, res.DataBase64, true); err != nil {
			return fmt.Errorf("export to %s (Compendium needs a grant for that path): %w", dest, err)
		}
	}
	return nil
}

type importSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type importResult struct {
	Imported []string     `json:"imported"`
	Skipped  []importSkip `json:"skipped"`
}

type importFile struct {
	name, title string
	data        []byte
}

// importPlan is what importNotes will do with one file, decided before
// anything is written.
type importPlan struct {
	f      importFile
	action string // create | overwrite | skip
	reason string
}

// planImport decides every file's fate exactly as the write loop will
// act on it, in order: invalid or oversized files, titles that exist
// (without overwrite), unchanged overwrites, later duplicates of a title
// already in this import, and titles that collide by Unicode
// normalization are all skipped; only creates and real overwrites count
// toward the vault limits.
func (v *vault) planImport(files []importFile, overwrite bool) (plans []importPlan, adds, grow int) {
	seenTitles, seenNorms := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		p := importPlan{f: f, action: "skip"}
		k := fold(f.title)
		switch n := v.notes[k]; {
		case validTitle(f.title) != nil:
			p.reason = validTitle(f.title).Error()
		case len(f.data) > MaxNoteBytes:
			p.reason = fmt.Sprintf("larger than %d bytes", MaxNoteBytes)
		case seenTitles[k] || seenNorms[normKey(f.title)]:
			p.reason = "another file in this import has the same title"
		case n != nil && !overwrite:
			p.reason = "a note with this title exists"
		case n != nil && n.content == string(f.data):
			p.reason = "identical to the existing note"
		case n != nil:
			p.action = "overwrite"
			grow += len(f.data) - len(n.content)
		case v.normClash(f.title, nil) != nil:
			p.reason = v.normClash(f.title, nil).Error()
		default:
			p.action = "create"
			adds++
			grow += len(f.data)
		}
		if validTitle(f.title) == nil {
			seenTitles[k], seenNorms[normKey(f.title)] = true, true
		}
		plans = append(plans, p)
	}
	return plans, adds, grow
}

// importNotes adds plain Markdown notes. The whole import is planned
// first and refused before any write if what it would actually write does
// not fit; then each note is created atomically, so re-running an
// interrupted import skips what already arrived.
func (v *vault) importNotes(ctx context.Context, files []importFile, overwrite bool) (importResult, error) {
	res := importResult{Imported: []string{}, Skipped: []importSkip{}}
	if err := v.ready(ctx); err != nil {
		return res, err
	}
	plans, adds, grow := v.planImport(files, overwrite)
	if len(v.notes)+adds > MaxNotes {
		return res, fmt.Errorf("import: %d new notes would exceed the %d-note vault limit; nothing was imported", adds, MaxNotes)
	}
	if v.bytes+grow > MaxVaultBytes {
		return res, fmt.Errorf("import: %d more bytes would exceed the %d-byte vault limit; nothing was imported", grow, MaxVaultBytes)
	}
	bytes := v.bytes
	for _, p := range plans {
		switch p.action {
		case "create":
			bytes += len(p.f.data)
		case "overwrite":
			bytes += len(p.f.data) - len(v.notes[fold(p.f.title)].content)
		}
		if bytes > MaxVaultBytes {
			return res, fmt.Errorf("import: writing %q in archive order would exceed the %d-byte vault limit; nothing was imported", p.f.title, MaxVaultBytes)
		}
	}
	for _, p := range plans {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		var err error
		switch p.action {
		case "skip":
			res.Skipped = append(res.Skipped, importSkip{p.f.name, p.reason})
			continue
		case "overwrite":
			_, err = v.save(ctx, p.f.title, string(p.f.data))
		default:
			_, err = v.create(ctx, p.f.title, string(p.f.data))
		}
		if err != nil {
			return res, err
		}
		res.Imported = append(res.Imported, p.f.title)
	}
	return res, nil
}

// Limits for one archive import. Sizes are what is actually inflated,
// counted while reading; the sizes an archive's headers claim are never
// trusted. There is deliberately no compression-ratio rule: ordinary
// repetitive Markdown (lists, tables) compresses past 100:1, and the
// per-entry and aggregate limits already bound the memory a bomb can take.
// exportVault never writes an archive beyond these limits.
const (
	maxImportEntries = MaxNotes
	maxImportBytes   = 32 << 20 // total inflated bytes retained or read
)

// readZip extracts flat .md entries from a bounded zip archive. Any limit
// breach rejects the whole archive before a single note is written.
func readZip(data []byte) ([]importFile, []importSkip, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("import: not a zip archive: %w", err)
	}
	if len(zr.File) > maxImportEntries {
		return nil, nil, fmt.Errorf("import: archive has %d entries; limit is %d", len(zr.File), maxImportEntries)
	}
	var files []importFile
	var skipped []importSkip
	total := 0
	for _, f := range zr.File {
		name := f.Name
		if f.FileInfo().IsDir() {
			continue
		}
		if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains("/"+name+"/", "/../") {
			skipped = append(skipped, importSkip{name, "unsafe path"})
			continue
		}
		base := path.Base(name)
		if !strings.HasSuffix(strings.ToLower(base), ".md") {
			skipped = append(skipped, importSkip{name, "not a .md file"})
			continue
		}
		if f.UncompressedSize64 > MaxNoteBytes {
			skipped = append(skipped, importSkip{name, fmt.Sprintf("larger than %d bytes", MaxNoteBytes)})
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, nil, fmt.Errorf("import %s: %w", name, err)
		}
		body, err := io.ReadAll(io.LimitReader(rc, MaxNoteBytes+1))
		rc.Close()
		total += len(body)
		if total > maxImportBytes {
			return nil, nil, fmt.Errorf("import: archive inflates to more than %d bytes; nothing was imported", maxImportBytes)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("import %s: %w; nothing was imported", name, err)
		}
		if len(body) > MaxNoteBytes {
			skipped = append(skipped, importSkip{name, fmt.Sprintf("larger than %d bytes", MaxNoteBytes)})
			continue
		}
		files = append(files, importFile{name: name, title: titleFromStem(base[:len(base)-3]), data: body})
	}
	return files, skipped, nil
}

func (v *vault) today(ctx context.Context) (*note, bool, error) {
	title := dailyTitle(v.now())
	if n := v.notes[fold(title)]; n != nil {
		return n, false, nil
	}
	n, err := v.create(ctx, title, "# "+title+"\n\n")
	return n, err == nil, err
}
