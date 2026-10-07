package compendium

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
)

// store is the only way Compendium touches storage: scoped fs/* service
// calls made with the app's own grant. Every mutation is one atomic VFS
// operation (fs/save stages and renames; fs/rename is a same-mount rename).
type store struct {
	call func(ctx context.Context, method string, params, out any) error
}

type dirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

func (s store) list(ctx context.Context, dir string) ([]dirEntry, error) {
	var out struct {
		Entries []dirEntry `json:"entries"`
	}
	if err := s.call(ctx, "fs/list", map[string]string{"path": dir}, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

func (s store) read(ctx context.Context, p string) ([]byte, error) {
	var out struct {
		DataBase64 string `json:"data_base64"`
	}
	if err := s.call(ctx, "fs/read", map[string]string{"path": p}, &out); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(out.DataBase64)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	return data, nil
}

func (s store) save(ctx context.Context, p string, data []byte, overwrite bool) error {
	return s.call(ctx, "fs/save", map[string]any{
		"path": p, "data_base64": base64.StdEncoding.EncodeToString(data), "overwrite": overwrite,
	}, nil)
}

func (s store) rename(ctx context.Context, src, dst string) error {
	return s.call(ctx, "fs/rename", map[string]any{"src": src, "dst": dst, "overwrite": false}, nil)
}

func (s store) remove(ctx context.Context, p string, recursive bool) error {
	return s.call(ctx, "fs/remove", map[string]any{"path": p, "recursive": recursive}, nil)
}

func (s store) mkdir(ctx context.Context, p string) error {
	return s.call(ctx, "fs/mkdir", map[string]string{"path": p}, nil)
}

// exists reports whether p exists by listing its parent, so a missing path
// is a definite answer rather than an error string to interpret. A missing
// parent is itself a definite "no".
func (s store) exists(ctx context.Context, p string) (bool, error) {
	if p == rootDir {
		return true, nil
	}
	parent := path.Dir(p)
	entries, err := s.list(ctx, parent)
	if err != nil {
		if ok, perr := s.exists(ctx, parent); perr == nil && !ok {
			return false, nil
		}
		return false, err
	}
	base := path.Base(p)
	for _, e := range entries {
		if e.Name == base {
			return true, nil
		}
	}
	return false, nil
}
