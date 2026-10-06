package vfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setupTestVFS(t *testing.T) (*VFS, *HostFS) {
	t.Helper()
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "apps", "data"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "shared", "workspace"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "shared", "notes.txt"), []byte("notes content"), 0o644))
	h, err := NewHost(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	v := New(h)
	return v, h
}

func setupTestMemVFS(t *testing.T) *VFS {
	t.Helper()
	m := NewMem()
	v := New(m)
	must(t, v.MkdirAll("/apps/data"))
	must(t, v.MkdirAll("/shared/workspace"))
	must(t, v.WriteFile("/shared/notes.txt", []byte("mem notes content"), 0o644))
	return v
}

func TestAppPrivateStorage_HostFS(t *testing.T) {
	v, _ := setupTestVFS(t)
	appA := v.ForApp("appA")
	appB := v.ForApp("appB")

	// AppA writes a private file
	err := appA.WriteFile("/apps/data/appA/secret.txt", []byte("secretA"), 0o644)
	if err != nil {
		t.Fatalf("appA write: %v", err)
	}

	// AppA can read its private file
	data, err := appA.ReadFile("/apps/data/appA/secret.txt")
	if err != nil || string(data) != "secretA" {
		t.Fatalf("appA read: %v, got %q", err, string(data))
	}

	// AppB CANNOT read AppA's private file
	_, err = appB.ReadFile("/apps/data/appA/secret.txt")
	if err == nil {
		t.Fatal("expected appB read of appA secret to fail")
	}
	var vErr *Error
	if !errors.As(err, &vErr) || vErr.Code != ErrPermission {
		t.Fatalf("expected ErrPermission, got %v", err)
	}

	// AppB CANNOT write into AppA's private directory
	err = appB.WriteFile("/apps/data/appA/hacked.txt", []byte("evil"), 0o644)
	if err == nil {
		t.Fatal("expected appB write to appA to fail")
	}

	// AppB CANNOT remove AppA's file
	err = appB.Remove("/apps/data/appA/secret.txt")
	if err == nil {
		t.Fatal("expected appB remove to fail")
	}

	// AppB CANNOT stat AppA's file
	_, err = appB.Stat("/apps/data/appA/secret.txt")
	if err == nil {
		t.Fatal("expected appB stat to fail")
	}

	// AppB CANNOT rename AppA's file
	err = appB.Rename("/apps/data/appA/secret.txt", "/apps/data/appB/stolen.txt")
	if err == nil {
		t.Fatal("expected appB rename to fail")
	}

	// AppB CANNOT copy AppA's file
	err = appB.Copy("/apps/data/appA/secret.txt", "/apps/data/appB/copy.txt", false)
	if err == nil {
		t.Fatal("expected appB copy to fail")
	}

	// AppB CANNOT move AppA's file
	err = appB.Move("/apps/data/appA/secret.txt", "/apps/data/appB/moved.txt", false)
	if err == nil {
		t.Fatal("expected appB move to fail")
	}

	// AppB cannot delete app private storage root
	err = appB.RemoveAll("/apps/data")
	if err == nil {
		t.Fatal("expected remove of /apps/data to fail")
	}
	err = appB.RemoveAll("/apps/data/appB")
	if err == nil {
		t.Fatal("expected remove of /apps/data/appB to fail")
	}

	// AppB listing /apps/data should only see appB, never appA
	must(t, appB.WriteFile("/apps/data/appB/own.txt", []byte("mine"), 0o644))
	entries, err := appB.ReadDir("/apps/data")
	if err != nil {
		t.Fatalf("appB ReadDir /apps/data: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "appA" {
			t.Fatalf("appB should not see appA directory in /apps/data")
		}
	}
	if len(entries) != 1 || entries[0].Name() != "appB" {
		t.Fatalf("appB should see exactly [appB], got %v", entries)
	}
}

func TestAppPrivateStorage_MemFS(t *testing.T) {
	v := setupTestMemVFS(t)
	appA := v.ForApp("appA")
	appB := v.ForApp("appB")

	// AppA writes a private file
	err := appA.WriteFile("/apps/data/appA/mem_secret.txt", []byte("memA"), 0o644)
	if err != nil {
		t.Fatalf("mem appA write: %v", err)
	}

	// AppB cannot read AppA's private file
	_, err = appB.ReadFile("/apps/data/appA/mem_secret.txt")
	if err == nil {
		t.Fatal("expected appB read to fail on MemFS")
	}
	var vErr *Error
	if !errors.As(err, &vErr) || vErr.Code != ErrPermission {
		t.Fatalf("expected ErrPermission on MemFS, got %v", err)
	}

	// AppB cannot write to AppA's private storage
	err = appB.WriteFile("/apps/data/appA/bad.txt", []byte("bad"), 0o644)
	if err == nil {
		t.Fatal("expected appB write to fail on MemFS")
	}

	// AppB listing /apps/data only sees appB
	must(t, appB.WriteFile("/apps/data/appB/b.txt", []byte("b"), 0o644))
	entries, err := appB.ReadDir("/apps/data")
	if err != nil {
		t.Fatalf("mem ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "appA" {
			t.Fatalf("mem appB should not see appA")
		}
	}
}

func TestScopedGrants_ReadOnlyAndReadWrite(t *testing.T) {
	v, _ := setupTestVFS(t)
	appA := v.ForApp("appA")
	appB := v.ForApp("appB")

	// Initially appA has no access to /shared/notes.txt
	_, err := appA.ReadFile("/shared/notes.txt")
	if err == nil {
		t.Fatal("expected appA to be denied without grant")
	}

	// Issue read-only grant for /shared/notes.txt to appA
	_, err = v.Grants().Issue("appA", "/shared/notes.txt", AccessRead, false)
	if err != nil {
		t.Fatalf("issue grant: %v", err)
	}

	// AppA can read now
	data, err := appA.ReadFile("/shared/notes.txt")
	if err != nil || string(data) != "notes content" {
		t.Fatalf("appA read after grant: %v, got %q", err, string(data))
	}

	// AppA CANNOT write or save (grant is read-only)
	err = appA.WriteFile("/shared/notes.txt", []byte("overwrite"), 0o644)
	if err == nil {
		t.Fatal("expected write to fail on read-only grant")
	}
	var vErr *Error
	if !errors.As(err, &vErr) || vErr.Code != ErrReadOnly {
		t.Fatalf("expected ErrReadOnly, got %v", err)
	}

	err = appA.SaveAtomic("/shared/notes.txt", []byte("atomic"), 0o644)
	if err == nil {
		t.Fatal("expected save to fail on read-only grant")
	}

	err = appA.Remove("/shared/notes.txt")
	if err == nil {
		t.Fatal("expected remove to fail on read-only grant")
	}

	// AppB still cannot read /shared/notes.txt
	_, err = appB.ReadFile("/shared/notes.txt")
	if err == nil {
		t.Fatal("expected appB to be denied")
	}

	// Issue read-write recursive grant for /shared/workspace to appB
	_, err = v.Grants().Issue("appB", "/shared/workspace", AccessReadWrite, true)
	if err != nil {
		t.Fatalf("issue recursive rw grant: %v", err)
	}

	// AppB can create subdirs and files inside /shared/workspace
	must(t, appB.MkdirAll("/shared/workspace/sub"))
	must(t, appB.WriteFile("/shared/workspace/sub/file.txt", []byte("hello"), 0o644))

	bData, err := appB.ReadFile("/shared/workspace/sub/file.txt")
	if err != nil || string(bData) != "hello" {
		t.Fatalf("read workspace file: %v, got %q", err, string(bData))
	}

	// AppB cannot write to /shared/workspace2 (prefix check boundary)
	err = appB.WriteFile("/shared/workspace2/evil.txt", []byte("evil"), 0o644)
	if err == nil {
		t.Fatal("expected write to /shared/workspace2 to fail")
	}

	// AppB cannot write to /shared/outside.txt
	err = appB.WriteFile("/shared/outside.txt", []byte("outside"), 0o644)
	if err == nil {
		t.Fatal("expected write to /shared/outside.txt to fail")
	}
}

func TestScopedGrants_Revocation(t *testing.T) {
	v, _ := setupTestVFS(t)
	appA := v.ForApp("appA")

	grant, err := v.Grants().Issue("appA", "/shared/notes.txt", AccessRead, false)
	if err != nil {
		t.Fatal(err)
	}

	// Read succeeds while grant is active
	_, err = appA.ReadFile("/shared/notes.txt")
	if err != nil {
		t.Fatalf("expected read to succeed: %v", err)
	}

	// Revoke the grant
	err = v.Grants().Revoke(grant.ID)
	if err != nil {
		t.Fatalf("revoke failed: %v", err)
	}

	// Read fails immediately
	_, err = appA.ReadFile("/shared/notes.txt")
	if err == nil {
		t.Fatal("expected read to fail immediately after revocation")
	}
	var vErr *Error
	if !errors.As(err, &vErr) || vErr.Code != ErrPermission {
		t.Fatalf("expected ErrPermission, got %v", err)
	}

	// Revoked status persists
	reloadedGrant, ok := v.Grants().Get(grant.ID)
	if !ok || !reloadedGrant.Revoked {
		t.Fatal("expected grant to remain revoked")
	}
}

func TestScopedGrants_ConfinementAndSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests require admin privileges on Windows")
	}
	v, h := setupTestVFS(t)
	appA := v.ForApp("appA")
	appB := v.ForApp("appB")

	// Setup private files
	must(t, appB.WriteFile("/apps/data/appB/secret.txt", []byte("topsecret"), 0o644))
	must(t, appA.WriteFile("/apps/data/appA/dummy.txt", []byte("dummy"), 0o644))

	// Create a malicious symlink on host inside appA's private directory pointing to appB's directory
	hostAppADir := filepath.Join(h.RootPath(), "apps", "data", "appA")
	hostAppBDir := filepath.Join(h.RootPath(), "apps", "data", "appB")
	symlinkPath := filepath.Join(hostAppADir, "evil_link")
	if err := os.Symlink(hostAppBDir, symlinkPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	// AppA attempting to read /apps/data/appA/evil_link/secret.txt must be rejected with ErrEscape
	_, err := appA.ReadFile("/apps/data/appA/evil_link/secret.txt")
	if err == nil {
		t.Fatal("expected symlink escape read to fail")
	}
	var vErr *Error
	if !errors.As(err, &vErr) || vErr.Code != ErrEscape {
		t.Fatalf("expected ErrEscape, got %v", err)
	}
}

func TestScopedGrants_CrossMount(t *testing.T) {
	v, _ := setupTestVFS(t)
	// Mount MemFS at /tmp
	must(t, v.Mount("/tmp", NewMem()))

	appA := v.ForApp("appA")

	// Issue rw grant on /tmp/scratch to appA
	_, err := v.Grants().Issue("appA", "/tmp/scratch", AccessReadWrite, true)
	if err != nil {
		t.Fatal(err)
	}

	// Write to MemFS
	must(t, appA.MkdirAll("/tmp/scratch"))
	must(t, appA.WriteFile("/tmp/scratch/temp.txt", []byte("transient"), 0o644))

	// Copy from MemFS to HostFS app-private storage
	err = appA.Copy("/tmp/scratch/temp.txt", "/apps/data/appA/saved.txt", false)
	if err != nil {
		t.Fatalf("cross-mount copy: %v", err)
	}

	data, err := appA.ReadFile("/apps/data/appA/saved.txt")
	if err != nil || string(data) != "transient" {
		t.Fatalf("read copied file: %v, got %q", err, string(data))
	}

	// Cross-mount rename should fail with ErrCrossMount
	err = appA.Rename("/tmp/scratch/temp.txt", "/apps/data/appA/renamed.txt")
	if err == nil {
		t.Fatal("expected cross-mount rename to fail")
	}
	var vErr *Error
	if !errors.As(err, &vErr) || vErr.Code != ErrCrossMount {
		t.Fatalf("expected ErrCrossMount, got %v", err)
	}

	// Cross-mount move should succeed
	err = appA.Move("/tmp/scratch/temp.txt", "/apps/data/appA/moved.txt", false)
	if err != nil {
		t.Fatalf("cross-mount move: %v", err)
	}

	// Original in MemFS should be gone
	_, err = appA.ReadFile("/tmp/scratch/temp.txt")
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected moved source to not exist, got %v", err)
	}
}

func TestScopedVFS_TrashIsolation(t *testing.T) {
	v, _ := setupTestVFS(t)
	appA := v.ForApp("appA")
	appB := v.ForApp("appB")

	must(t, appA.WriteFile("/apps/data/appA/fileA.txt", []byte("contentA"), 0o644))
	must(t, appB.WriteFile("/apps/data/appB/fileB.txt", []byte("contentB"), 0o644))

	// AppA and AppB trash their files
	entryA, err := appA.Trash("/apps/data/appA/fileA.txt")
	if err != nil {
		t.Fatalf("appA trash: %v", err)
	}
	entryB, err := appB.Trash("/apps/data/appB/fileB.txt")
	if err != nil {
		t.Fatalf("appB trash: %v", err)
	}

	// AppA list trash: only sees entryA
	listA, err := appA.ListTrash()
	if err != nil {
		t.Fatalf("appA list trash: %v", err)
	}
	if len(listA) != 1 || listA[0].ID != entryA.ID {
		t.Fatalf("appA expected [entryA], got %v", listA)
	}

	// AppB list trash: only sees entryB
	listB, err := appB.ListTrash()
	if err != nil {
		t.Fatalf("appB list trash: %v", err)
	}
	if len(listB) != 1 || listB[0].ID != entryB.ID {
		t.Fatalf("appB expected [entryB], got %v", listB)
	}

	// AppA cannot restore AppB's trash item
	_, err = appA.Restore(entryB.ID, "", false)
	if err == nil {
		t.Fatal("expected appA restore of appB item to fail")
	}

	// AppA emptying trash only removes entryA
	count, err := appA.EmptyTrash()
	if err != nil || count != 1 {
		t.Fatalf("appA empty trash: count=%d, err=%v", count, err)
	}

	// AppB's trash item remains intact
	listBAfter, err := appB.ListTrash()
	if err != nil || len(listBAfter) != 1 || listBAfter[0].ID != entryB.ID {
		t.Fatalf("appB item should still exist after appA emptied trash, got %v", listBAfter)
	}
}
