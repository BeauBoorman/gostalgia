package services

import (
	"context"
	"encoding/base64"
	"fmt"

	"gostalgia/internal/ipc"
	"gostalgia/internal/security"
	"gostalgia/internal/service"
)

// FSService exposes the virtual filesystem over IPC. It owns "fs/*".
type FSService struct {
	ctx *service.Context
}

func NewFS() *FSService { return &FSService{} }

func (s *FSService) Name() string      { return "fs" }
func (s *FSService) Depends() []string { return nil }

func (s *FSService) Init(ctx *service.Context) error {
	s.ctx = ctx
	return nil
}

func (s *FSService) Start(ctx context.Context) error {
	for method, h := range map[string]ipc.Handler{
		"fs/list":   s.list,
		"fs/read":   s.read,
		"fs/write":  s.write,
		"fs/mkdir":  s.mkdir,
		"fs/remove": s.remove,
	} {
		if err := s.ctx.Router.Handle(method, h); err != nil {
			return err
		}
	}
	return nil
}

func (s *FSService) Stop(ctx context.Context) error {
	s.ctx.Router.UnhandlePrefix("fs/")
	return nil
}

type fsEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
}

func (s *FSService) list(ctx context.Context, req ipc.Request) (any, error) {
	if err := ipc.RequireCap(ctx, security.CapFileRead); err != nil {
		return nil, err
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := ipc.DecodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		p.Path = "/"
	}
	entries, err := s.ctx.VFS.ReadDir(p.Path)
	if err != nil {
		return nil, err
	}
	out := make([]fsEntry, 0, len(entries))
	for _, e := range entries {
		mode := e.Type().String()
		size := int64(0)
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				size = info.Size()
			}
		}
		out = append(out, fsEntry{Name: e.Name(), IsDir: e.IsDir(), Size: size, Mode: mode})
	}
	return map[string]any{"path": p.Path, "entries": out}, nil
}

func (s *FSService) read(ctx context.Context, req ipc.Request) (any, error) {
	if err := ipc.RequireCap(ctx, security.CapFileRead); err != nil {
		return nil, err
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := ipc.DecodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	data, err := s.ctx.VFS.ReadFile(p.Path)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":        p.Path,
		"size":        len(data),
		"data_base64": base64.StdEncoding.EncodeToString(data),
	}, nil
}

func (s *FSService) write(ctx context.Context, req ipc.Request) (any, error) {
	if err := ipc.RequireCap(ctx, security.CapFileWrite); err != nil {
		return nil, err
	}
	var p struct {
		Path string `json:"path"`
		Data string `json:"data_base64"`
	}
	if err := ipc.DecodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, fmt.Errorf("params.path is required")
	}
	var data []byte
	if p.Data != "" {
		var err error
		data, err = base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			return nil, fmt.Errorf("params.data_base64: %w", err)
		}
	}
	if err := s.ctx.VFS.WriteFile(p.Path, data, 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"path": p.Path, "written": len(data)}, nil
}

func (s *FSService) mkdir(ctx context.Context, req ipc.Request) (any, error) {
	if err := ipc.RequireCap(ctx, security.CapFileWrite); err != nil {
		return nil, err
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := ipc.DecodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, fmt.Errorf("params.path is required")
	}
	if err := s.ctx.VFS.MkdirAll(p.Path); err != nil {
		return nil, err
	}
	return map[string]any{"path": p.Path, "created": true}, nil
}

func (s *FSService) remove(ctx context.Context, req ipc.Request) (any, error) {
	if err := ipc.RequireCap(ctx, security.CapFileWrite); err != nil {
		return nil, err
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := ipc.DecodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, fmt.Errorf("params.path is required")
	}
	if err := s.ctx.VFS.Remove(p.Path); err != nil {
		return nil, err
	}
	return map[string]any{"path": p.Path, "removed": true}, nil
}
