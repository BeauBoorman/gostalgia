package vfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

// validFSName enforces the io/fs name contract on the raw backends:
// names arrive here already in fs form (no leading slash, no dot
// segments) and must stay that way. Backslashes are rejected on every
// platform: they are ordinary filename characters on Unix but path
// separators on Windows, and one portable contract is easier to reason
// about than two.
func validFSName(op, name string) error {
	if strings.ContainsRune(name, '\\') {
		return &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}
	return nil
}

// HostFS is an FS backed by a directory on the host. All operations go
// through os.Root, so a name that escapes the backing directory — through
// ".." elements or symlinks — fails closed. Errors reference environment
// paths, never host paths.
type HostFS struct {
	root     *os.Root
	rootPath string
}

// NewHost opens dir as the backing directory for a HostFS.
func NewHost(dir string) (*HostFS, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("vfs: open host root %s: %w", dir, err)
	}
	return &HostFS{root: r, rootPath: dir}, nil
}

// RootPath returns the backing host directory.
func (h *HostFS) RootPath() string { return h.rootPath }

// Close releases the backing root handle.
func (h *HostFS) Close() error { return h.root.Close() }

func (h *HostFS) Open(name string) (fs.File, error) {
	if err := validFSName("open", name); err != nil {
		return nil, err
	}
	return h.root.Open(name)
}

func (h *HostFS) Stat(name string) (fs.FileInfo, error) {
	if err := validFSName("stat", name); err != nil {
		return nil, err
	}
	return h.root.Lstat(name)
}

func (h *HostFS) ReadFile(name string) ([]byte, error) {
	if err := validFSName("read", name); err != nil {
		return nil, err
	}
	f, err := h.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (h *HostFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := validFSName("readdir", name); err != nil {
		return nil, err
	}
	f, err := h.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return entries, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	// Back each entry's Info with the same Lstat source Stat uses. On
	// Windows, directory metadata read through an open enumeration
	// handle differs from Lstat of the same directory; one source keeps
	// entry.Info() == Stat() true everywhere.
	for i, e := range entries {
		child := e.Name()
		if name != "." {
			child = name + "/" + e.Name()
		}
		if info, err := h.root.Lstat(child); err == nil {
			entries[i] = hostEntry{DirEntry: e, info: info}
		}
	}
	return entries, nil
}

// hostEntry overrides DirEntry.Info with an Lstat-backed FileInfo.
type hostEntry struct {
	fs.DirEntry
	info fs.FileInfo
}

func (he hostEntry) Info() (fs.FileInfo, error) { return he.info, nil }

func (h *HostFS) MkdirAll(name string) error {
	if err := validFSName("mkdir", name); err != nil {
		return err
	}
	if name == "." {
		return nil
	}
	return h.root.MkdirAll(name, 0o755)
}

func (h *HostFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	if err := validFSName("write", name); err != nil {
		return err
	}
	if dir := path.Dir(name); dir != "." {
		if err := h.root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if perm == 0 {
		perm = 0o644
	}
	f, err := h.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

func (h *HostFS) Remove(name string) error {
	if err := validFSName("remove", name); err != nil {
		return err
	}
	if name == "." {
		return &fs.PathError{Op: "remove", Path: name, Err: errors.New("cannot remove the root")}
	}
	return h.root.Remove(name)
}
