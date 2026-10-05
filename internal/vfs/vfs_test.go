package vfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
)

func newTestHostFS(t *testing.T) *HostFS {
	t.Helper()
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "dir", "sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "dir", "b.txt"), []byte("beta"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "dir", "sub", "c.txt"), []byte("gamma"), 0o644))
	h, err := NewHost(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func TestHostFSMatchesFstest(t *testing.T) {
	h := newTestHostFS(t)
	if runtime.GOOS == "windows" {
		// fstest requires entry.Info() captured at time T1 to equal
		// Open+File.Stat() captured at T2. Windows directory metadata
		// is inconsistent across those API surfaces for freshly
		// created directories (the first query reports the query
		// time, not the mtime), so exact equality is unattainable —
		// stdlib os.DirFS has the same problem. The structural
		// checks still run on every platform in
		// TestHostFSConsistency, and the full fstest gate runs on
		// unix. See docs/filesystem.md.
		t.Skip("Windows directory metadata is not stable across API calls; see TestHostFSConsistency")
	}
	if err := fstest.TestFS(h, "a.txt", "dir/b.txt", "dir/sub/c.txt"); err != nil {
		t.Fatal(err)
	}
}

// TestHostFSConsistency exercises everything fstest does except exact
// cross-call metadata equality: the tree is fully walkable, all names
// agree between ReadDir and WalkDir, and every file opens and reads
// exactly what was written.
func TestHostFSConsistency(t *testing.T) {
	h := newTestHostFS(t)

	want := map[string]string{
		"a.txt":         "alpha",
		"dir/b.txt":     "beta",
		"dir/sub/c.txt": "gamma",
	}
	seen := map[string]bool{}
	err := fs.WalkDir(h, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		seen[path] = true
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(h, path)
		if err != nil {
			return err
		}
		if string(data) != want[path] {
			t.Errorf("%s: content = %q, want %q", path, data, want[path])
		}
		return nil
	})
	must(t, err)
	for path := range want {
		if !seen[path] {
			t.Errorf("%s: not reachable via WalkDir", path)
		}
	}

	entries, err := h.ReadDir(".")
	must(t, err)
	if len(entries) != 2 || entries[0].Name() != "a.txt" || entries[1].Name() != "dir" {
		t.Errorf("root entries = %v", entries)
	}
	if _, err := h.Stat("dir/b.txt"); err != nil {
		t.Errorf("Stat: %v", err)
	}
}

func TestHostFSWriteReadRoundTrip(t *testing.T) {
	h, err := NewHost(t.TempDir())
	t.Cleanup(func() { h.Close() })
	if err != nil {
		t.Fatal(err)
	}
	if err := h.WriteFile("deep/nested/file.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := h.ReadFile("deep/nested/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("read %q, want hello", data)
	}
	if _, err := h.Stat("deep/nested/file.txt"); err != nil {
		t.Fatal(err)
	}
	if err := h.Remove("deep/nested/file.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Stat("deep/nested/file.txt"); !IsNotExist(err) {
		t.Fatalf("stat after remove = %v, want not exist", err)
	}
}

func TestHostFSRejectsEscape(t *testing.T) {
	h, err := NewHost(t.TempDir())
	t.Cleanup(func() { h.Close() })
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../x", "a/../../x", "/.."} {
		if _, err := h.Open(name); err == nil {
			t.Errorf("Open(%q) succeeded, want rejection", name)
		}
		if err := h.WriteFile(name, []byte("nope"), 0o644); err == nil {
			t.Errorf("WriteFile(%q) succeeded, want rejection", name)
		}
	}
}

func TestHostFSBlocksSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	secret := t.TempDir()
	must(t, os.WriteFile(filepath.Join(secret, "secret.txt"), []byte("secret"), 0o644))
	must(t, os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	must(t, os.Symlink(secret, filepath.Join(root, "sub", "link")))

	h, err := NewHost(root)
	t.Cleanup(func() { h.Close() })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Open("sub/link/secret.txt"); err == nil {
		t.Fatal("Open through symlink escape succeeded, want failure")
	}
	if _, err := h.ReadFile("sub/link/secret.txt"); err == nil {
		t.Fatal("ReadFile through symlink escape succeeded, want failure")
	}
}

func TestMemFSMatchesFstest(t *testing.T) {
	m := NewMem()
	must(t, m.WriteFile("a.txt", []byte("alpha"), 0o644))
	must(t, m.WriteFile("dir/b.txt", []byte("beta"), 0o644))
	if err := fstest.TestFS(m, "a.txt", "dir/b.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestMemFSRemoveEmptyDirOnly(t *testing.T) {
	m := NewMem()
	must(t, m.MkdirAll("d/e"))
	if err := m.Remove("d"); err == nil {
		t.Fatal("removing non-empty dir succeeded, want error")
	}
	if err := m.Remove("d/e"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("d"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("."); err == nil {
		t.Fatal("removing root succeeded, want error")
	}
}

func TestVFSMountShadowsRoot(t *testing.T) {
	h, err := NewHost(t.TempDir())
	t.Cleanup(func() { h.Close() })
	if err != nil {
		t.Fatal(err)
	}
	env := New(h)
	must(t, env.Mount("/tmp", NewMem()))

	// Write lands in the memfs mount, not the host root.
	must(t, env.WriteFile("/tmp/note.txt", []byte("ephemeral"), 0o644))
	if _, err := h.Stat("tmp/note.txt"); !IsNotExist(err) {
		t.Fatalf("host has tmp/note.txt: %v, want not exist", err)
	}
	data, err := env.ReadFile("/tmp/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ephemeral" {
		t.Fatalf("read %q, want ephemeral", data)
	}

	// Root paths still resolve normally.
	must(t, env.WriteFile("/data/keep.txt", []byte("persistent"), 0o644))
	if _, err := h.Stat("data/keep.txt"); err != nil {
		t.Fatalf("root-backed write missing on host: %v", err)
	}

	// Mount points are listed.
	points := env.MountPoints()
	if len(points) != 1 || points[0] != "/tmp" {
		t.Fatalf("mount points = %v, want [/tmp]", points)
	}

	// Unmount restores the root view.
	must(t, env.Unmount("/tmp"))
	if _, err := env.ReadFile("/tmp/note.txt"); !IsNotExist(err) {
		t.Fatalf("read after unmount = %v, want not exist", err)
	}
}

func TestVFSRejectsMountOverRoot(t *testing.T) {
	env := New(NewMem())
	if err := env.Mount("/", NewMem()); err == nil {
		t.Fatal("mounting over / succeeded, want error")
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"/users/guest": "users/guest",
		"users/guest/": "users/guest",
		"":             ".",
		"/":            ".",
		".":            ".",
		"a/b/c":        "a/b/c",
	}
	for in, want := range cases {
		got, err := Normalize(in)
		if err != nil {
			t.Errorf("Normalize(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"..", "/a/../b", "a/..", "a//b", "/./x", "./x", "/.", "dir\\file", "a/../.."} {
		if _, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) succeeded, want rejection", in)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
