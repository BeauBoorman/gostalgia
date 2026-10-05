package vfs

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// VFS is the environment filesystem: a root FS plus a mount table whose
// entries shadow subtrees of the root. Applications receive a *VFS and
// only ever speak environment paths.
type VFS struct {
	root   FS
	mu     sync.RWMutex
	mounts map[string]FS // fs-style mount point ("tmp") -> fs
}

func New(root FS) *VFS {
	return &VFS{root: root, mounts: map[string]FS{}}
}

// Mount mounts f at path (environment form, e.g. "/tmp"), shadowing
// whatever the root has there.
func (v *VFS) Mount(path string, f FS) error {
	name, err := Normalize(path)
	if err != nil {
		return err
	}
	if name == "." {
		return errors.New("vfs: cannot mount over the root")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, exists := v.mounts[name]; exists {
		return fmt.Errorf("vfs: %s is already mounted", path)
	}
	v.mounts[name] = f
	return nil
}

// Unmount removes the mount at path.
func (v *VFS) Unmount(path string) error {
	name, err := Normalize(path)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.mounts[name]; !ok {
		return fmt.Errorf("vfs: nothing is mounted at %s", path)
	}
	delete(v.mounts, name)
	return nil
}

// MountPoints lists active mount points in environment form.
func (v *VFS) MountPoints() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]string, 0, len(v.mounts))
	for mp := range v.mounts {
		out = append(out, "/"+mp)
	}
	sort.Strings(out)
	return out
}

// Root returns the root filesystem (used for status reporting only).
func (v *VFS) Root() FS { return v.root }

func (v *VFS) resolve(name string) (FS, string) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if name != "." {
		var best string
		for mp := range v.mounts {
			if name == mp || strings.HasPrefix(name, mp+"/") {
				if len(mp) > len(best) {
					best = mp
				}
			}
		}
		if best != "" {
			if name == best {
				return v.mounts[best], "."
			}
			return v.mounts[best], strings.TrimPrefix(name, best+"/")
		}
	}
	return v.root, name
}

func (v *VFS) Open(name string) (fs.File, error) {
	name, err := Normalize(name)
	if err != nil {
		return nil, err
	}
	fsys, sub := v.resolve(name)
	return fsys.Open(sub)
}

func (v *VFS) Stat(name string) (fs.FileInfo, error) {
	name, err := Normalize(name)
	if err != nil {
		return nil, err
	}
	fsys, sub := v.resolve(name)
	return fsys.Stat(sub)
}

func (v *VFS) ReadFile(name string) ([]byte, error) {
	name, err := Normalize(name)
	if err != nil {
		return nil, err
	}
	fsys, sub := v.resolve(name)
	return fsys.ReadFile(sub)
}

func (v *VFS) ReadDir(name string) ([]fs.DirEntry, error) {
	name, err := Normalize(name)
	if err != nil {
		return nil, err
	}
	fsys, sub := v.resolve(name)
	return fsys.ReadDir(sub)
}

func (v *VFS) MkdirAll(name string) error {
	name, err := Normalize(name)
	if err != nil {
		return err
	}
	fsys, sub := v.resolve(name)
	return fsys.MkdirAll(sub)
}

func (v *VFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	name, err := Normalize(name)
	if err != nil {
		return err
	}
	fsys, sub := v.resolve(name)
	return fsys.WriteFile(sub, data, perm)
}

func (v *VFS) Remove(name string) error {
	name, err := Normalize(name)
	if err != nil {
		return err
	}
	fsys, sub := v.resolve(name)
	return fsys.Remove(sub)
}
