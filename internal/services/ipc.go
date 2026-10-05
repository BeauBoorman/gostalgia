package services

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"gostalgia/internal/ipc"
	"gostalgia/internal/service"
	"gostalgia/platform"
)

// runtimeFile records how a live environment is reached. gctl reads it.
type runtimeFile struct {
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	Endpoint  string `json:"endpoint"`
	Token     string `json:"token"`
	StartedAt string `json:"started_at"`
}

// IPCService owns the environment's socket listener. It is the bridge
// between the in-process router and local clients (gctl, and later
// out-of-process applications). It owns the "ipc" service name.
type IPCService struct {
	ctx      *service.Context
	log      *slog.Logger
	listener net.Listener
	server   *ipc.Server
	endpoint string
}

func NewIPC() *IPCService { return &IPCService{} }

func (s *IPCService) Name() string      { return "ipc" }
func (s *IPCService) Depends() []string { return nil }

func (s *IPCService) Init(ctx *service.Context) error {
	s.ctx = ctx
	s.log = ctx.Log.With("service", "ipc")
	return nil
}

func (s *IPCService) Start(ctx context.Context) error {
	ln, endpoint, err := platform.ListenIPC(s.ctx.Root)
	if err != nil {
		return err
	}
	s.listener = ln
	s.endpoint = endpoint

	s.server = ipc.NewServer(ln, s.ctx.Router, s.ctx.Token, s.log)
	go func() {
		if err := s.server.Serve(); err != nil {
			s.log.Error("ipc server stopped with error", "err", err)
		}
	}()

	s.ctx.Endpoint = endpoint
	if err := s.writeRuntimeFile(); err != nil {
		return err
	}
	s.log.Info("ipc listening", "endpoint", endpoint)
	return nil
}

func (s *IPCService) Stop(ctx context.Context) error {
	if s.server != nil {
		if err := s.server.Close(); err != nil {
			s.log.Warn("ipc server close", "err", err)
		}
	}
	if s.listener != nil {
		s.listener.Close()
	}
	_ = os.Remove(s.runtimeFilePath())
	s.log.Info("ipc stopped", "endpoint", s.endpoint)
	return nil
}

// Endpoint returns the listening endpoint (empty before Start).
func (s *IPCService) Endpoint() string { return s.endpoint }

func (s *IPCService) runtimeFilePath() string {
	return filepath.Join(s.ctx.Root, "runtime.json")
}

func (s *IPCService) writeRuntimeFile() error {
	data, err := json.MarshalIndent(runtimeFile{
		PID:       os.Getpid(),
		Version:   s.ctx.Version,
		Endpoint:  s.endpoint,
		Token:     s.ctx.Token,
		StartedAt: s.ctx.BootedAt.UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the file embeds the IPC token; it is local trust, not a
	// security boundary, but it should not be world-readable.
	return os.WriteFile(s.runtimeFilePath(), append(data, '\n'), 0o600)
}
