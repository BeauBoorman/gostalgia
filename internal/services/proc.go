package services

import (
	"context"
	"fmt"
	"time"

	"gostalgia/internal/ipc"
	"gostalgia/internal/security"
	"gostalgia/internal/service"
)

// ProcService exposes the process manager over IPC. It owns "proc/*".
type ProcService struct {
	ctx *service.Context
}

func NewProc() *ProcService { return &ProcService{} }

func (s *ProcService) Name() string      { return "process" }
func (s *ProcService) Depends() []string { return nil }

func (s *ProcService) Init(ctx *service.Context) error {
	s.ctx = ctx
	return nil
}

func (s *ProcService) Start(ctx context.Context) error {
	if err := s.ctx.Router.Handle("proc/list", s.list); err != nil {
		return err
	}
	return s.ctx.Router.Handle("proc/stop", s.stop)
}

func (s *ProcService) Stop(ctx context.Context) error {
	s.ctx.Router.UnhandlePrefix("proc/")
	return nil
}

func (s *ProcService) list(ctx context.Context, req ipc.Request) (any, error) {
	if err := ipc.RequireCap(ctx, security.CapProcList); err != nil {
		return nil, err
	}
	return s.ctx.Procs.List(), nil
}

func (s *ProcService) stop(ctx context.Context, req ipc.Request) (any, error) {
	if err := ipc.RequireCap(ctx, security.CapProcStop); err != nil {
		return nil, err
	}
	var p struct {
		ID      int32 `json:"id"`
		Timeout int   `json:"timeout_seconds,omitempty"`
	}
	if err := ipc.DecodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.ID == 0 {
		return nil, fmt.Errorf("params.id is required")
	}
	timeout := 5 * time.Second
	if p.Timeout > 0 {
		timeout = time.Duration(p.Timeout) * time.Second
	}
	if err := s.ctx.Procs.Stop(p.ID, timeout); err != nil {
		return nil, err
	}
	return map[string]any{"id": p.ID, "stopped": true}, nil
}
