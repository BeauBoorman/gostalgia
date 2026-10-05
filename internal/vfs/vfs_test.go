package vfs

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestHostFSMatchesFstest(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "dir", "sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "dir", "b.txt"), []byte("beta"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "dir", "sub", "c.txt"), []byte("gamma"), 0o644))

	h, err := NewHost(root)
	t.Cleanup(func() { h.Close() })
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(h, "a.txt", "dir/b.txt", "dir/sub/c.txt"); err != nil {
		t.Fatal(err)
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
