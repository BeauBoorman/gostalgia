package compendium

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gostalgia/internal/vfs"
	"gostalgia/sdk"
)

// hostAdapter serves Compendium's fs/* calls from the real host-backed VFS
// (os.Root, staged atomic saves) so failure injection exercises the actual
// stage → fsync → rename sequence rather than a model of it. Test-only:
// production Compendium reaches storage solely through sdk.Context.Call.
type hostAdapter struct {
	mu   sync.Mutex
	dir  string
	host *vfs.HostFS
	v    *vfs.VFS
	dead bool
}

func newHostAdapter(t *testing.T, dir string) *hostAdapter {
	t.Helper()
	host, err := vfs.NewHost(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	return &hostAdapter{dir: dir, host: host, v: vfs.New(host)}
}

func (a *hostAdapter) call(_ context.Context, method string, params, out any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dead {
		return errCrashed
	}
	raw, _ := json.Marshal(params)
	var p struct {
		Path      string `json:"path"`
		Src       string `json:"src"`
		Dst       string `json:"dst"`
		Data      string `json:"data_base64"`
		Overwrite *bool  `json:"overwrite"`
		Recursive bool   `json:"recursive"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if !strings.HasPrefix(p.Path+p.Src, testRoot) {
		return fmt.Errorf("permission denied: %s", p.Path+p.Src)
	}
	reply := func(v any) error {
		if out == nil {
			return nil
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
	switch method {
	case "fs/list":
		entries, err := a.v.ReadDir(p.Path)
		if err != nil {
			return err
		}
		type entry struct {
			Name  string `json:"name"`
			IsDir bool   `json:"is_dir"`
			Size  int64  `json:"size"`
		}
		list := []entry{}
		for _, e := range entries {
			var size int64
			if info, err := e.Info(); err == nil && !e.IsDir() {
				size = info.Size()
			}
			list = append(list, entry{e.Name(), e.IsDir(), size})
		}
		return reply(map[string]any{"entries": list})
	case "fs/read":
		data, err := a.v.ReadFile(p.Path)
		if err != nil {
			return err
		}
		return reply(map[string]any{"data_base64": base64.StdEncoding.EncodeToString(data)})
	case "fs/save":
		data, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			return err
		}
		overwrite := p.Overwrite == nil || *p.Overwrite
		return a.v.SaveAtomicOpt(p.Path, data, 0o644, overwrite)
	case "fs/rename":
		return a.v.RenameOpt(p.Src, p.Dst, false)
	case "fs/remove":
		if p.Recursive {
			return a.v.RemoveAll(p.Path)
		}
		return a.v.Remove(p.Path)
	case "fs/mkdir":
		return a.v.MkdirAll(p.Path)
	}
	return fmt.Errorf("hostAdapter: unsupported method %s", method)
}

func openHostHarness(t *testing.T, a *hostAdapter) *harness {
	t.Helper()
	inst, _ := Factory()
	h := &harness{handlers: map[string]sdk.Handler{}}
	h.app = inst.(*Compendium)
	ctx := sdk.NewContext(Manifest(), slog.New(slog.NewTextHandler(io.Discard, nil)), a.call,
		func(name string, hd sdk.Handler) error { h.handlers[name] = hd; return nil })
	if err := h.app.Init(ctx); err != nil {
		t.Fatal(err)
	}
	return h
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestHostFSAtomicWriteFailureInjection kills a save inside the real
// HostFS atomic write: once while the staging file is being written (torn
// staging file left behind, as a kill -9 would) and once at the final
// rename (the VFS preserves the staged bytes as .recover), at every
// fs/save the operation performs. Reopening must keep committed content,
// rebuild a consistent index, and recover or cleanly drop the edit.
func TestHostFSAtomicWriteFailureInjection(t *testing.T) {
	const oldBody = "Old [[Beta]] #old\n"
	const newBody = "New [[Gamma]] #new\n"
	seedDir := t.TempDir()
	seed := openHostHarness(t, newHostAdapter(t, seedDir))
	seed.must(t, "create", map[string]string{"title": "Beta"}, nil)
	seed.must(t, "create", map[string]string{"title": "Gamma"}, nil)
	seed.must(t, "create", map[string]string{"title": "Alpha", "content": oldBody}, nil)
	seed.must(t, "edit", map[string]string{"title": "Alpha", "content": newBody}, nil)

	// Count the fs/save calls a successful save makes.
	countDir := t.TempDir()
	copyTree(t, seedDir, countDir)
	ca := newHostAdapter(t, countDir)
	saves := 0
	ca.host.SetSaveHooks(func(string) error { saves++; return nil }, nil)
	openHostHarness(t, ca).must(t, "save", map[string]string{"title": "Alpha", "content": newBody}, nil)
	if saves < 3 {
		t.Fatalf("save made only %d atomic writes", saves)
	}

	for k := 1; k <= saves; k++ {
		for _, phase := range []string{"stage", "rename"} {
			t.Run(fmt.Sprintf("write%d_%s", k, phase), func(t *testing.T) {
				dir := t.TempDir()
				copyTree(t, seedDir, dir)
				a := newHostAdapter(t, dir)
				n := 0
				stage := func(stageName string) error {
					if n++; n == k && phase == "stage" {
						// A kill mid-write leaves a partial staging file.
						partial := fmt.Sprintf("%s.tmp.%d.%d", strings.SplitN(stageName, ".tmp.", 2)[0], 1, 1)
						_ = os.WriteFile(filepath.Join(dir, filepath.FromSlash(partial)), []byte("par"), 0o644)
						a.dead = true // the process is gone: nothing after this runs
						return errCrashed
					}
					return nil
				}
				rename := func(string, string) error {
					if n == k && phase == "rename" {
						a.dead = true
						return errCrashed
					}
					return nil
				}
				a.host.SetSaveHooks(stage, rename)
				victim := openHostHarness(t, a)
				victim.must(t, "status", nil, nil)
				_ = victim.call("save", map[string]string{"title": "Alpha", "content": newBody}, nil)

				fresh := newHostAdapter(t, dir)
				h := openHostHarness(t, fresh)
				var st statusOut
				h.must(t, "status", nil, &st)
				var a1 noteOut
				h.must(t, "open", map[string]string{"title": "Alpha"}, &a1)
				var d draftsOut
				h.must(t, "drafts", nil, &d)
				switch a1.Content {
				case oldBody:
					if len(d.Drafts) != 1 || d.Drafts[0].Content != newBody {
						t.Fatalf("uncommitted edit not recoverable: %+v", d.Drafts)
					}
				case newBody:
					if len(d.Drafts) != 0 {
						t.Fatalf("committed edit left a draft: %+v", d.Drafts)
					}
				default:
					t.Fatalf("committed content corrupted: %q", a1.Content)
				}
				var gamma noteOut
				h.must(t, "open", map[string]string{"title": "Gamma"}, &gamma)
				if contains(titles(gamma.Backlinks), "Alpha") != (a1.Content == newBody) {
					t.Fatal("index disagrees with committed content")
				}
				_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
					if err == nil && (strings.Contains(info.Name(), ".tmp.") || strings.HasSuffix(info.Name(), ".recover")) {
						t.Errorf("artifact left after reopen: %s", path.Base(p))
					}
					return nil
				})
				// Third open: index already consistent.
				h3 := openHostHarness(t, newHostAdapter(t, dir))
				h3.must(t, "status", nil, &st)
				if st.Index.Rebuilt || st.Index.Reparsed != 0 {
					t.Fatalf("index not persisted consistently: %+v", st.Index)
				}
			})
		}
	}
}
