// Package vfs implements the environment's virtual filesystem. Paths are
// environment paths ("/users/guest/documents"); host paths never leak
// through the API. The root is backed by a host directory (confined with
// os.Root, so symlink and ".." escapes fail closed), and a mount table
// lets subtrees (memory filesystems, future remote filesystems) shadow it.
package vfs

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// FS is the environment filesystem interface: io/fs read semantics plus
// the writes the environment needs. Path arguments accept both
// environment form ("/users/guest") and io/fs form ("users/guest").
type FS interface {
	fs.FS
	fs.StatFS
	fs.ReadDirFS
	fs.ReadFileFS

	// MkdirAll creates the directory and any missing parents.
	MkdirAll(path string) error
	// WriteFile creates or truncates path, creating parents as needed.
	WriteFile(path string, data []byte, perm fs.FileMode) error
	// Remove deletes a file or an empty directory.
	Remove(path string) error
}

// Normalize converts an environment path ("/users/guest") to io/fs form
// ("users/guest"). Environment paths may carry a leading slash, but dot
// segments, backslashes, and interior empty elements are rejected outright
// — a path may never leave or obfuscate its position in the tree.
func Normalize(path string) (string, error) {
	if strings.ContainsRune(path, '\\') {
		return "", fmt.Errorf("vfs: path %q contains a backslash; use '/' separators", path)
	}
	if path == "." {
		return ".", nil // the root, already in fs form
	}
	segs := strings.Split(path, "/")
	out := make([]string, 0, len(segs))
	for i, seg := range segs {
		switch {
		case seg == "":
			if i == 0 || i == len(segs)-1 {
				continue // leading or trailing slash
			}
			return "", fmt.Errorf("vfs: invalid path %q", path)
		case seg == "." || seg == "..":
			return "", fmt.Errorf("vfs: invalid path %q: dot segments are not allowed", path)
		default:
			if strings.ContainsRune(seg, 0) {
				return "", fmt.Errorf("vfs: path %q contains a NUL byte", path)
			}
			out = append(out, seg)
		}
	}
	if len(out) == 0 {
		return ".", nil
	}
	return strings.Join(out, "/"), nil
}

// IsNotExist reports whether err is a not-exist error from any FS.
func IsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
