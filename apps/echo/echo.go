// Package echo implements com.gostalgia.echo — the environment's first
// application. It demonstrates the full application contract: declared by
// a manifest, launched and supervised by the runtime, serving its own IPC
// methods, and stopping cleanly when its process is stopped.
package echo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gostalgia/internal/app"
	"gostalgia/internal/ipc"
	"gostalgia/internal/process"
	"gostalgia/internal/security"
)

const (
	ID         = "com.gostalgia.echo"
	Entrypoint = "echo"
)

// Manifest returns the application's manifest. The same document is
// seeded into /apps/manifests so the on-disk manifest format is
// exercised end to end.
func Manifest() app.Manifest {
	return app.Manifest{
		ID:          ID,
		Name:        "Echo",
		Version:     "0.1.0",
		Entrypoint:  Entrypoint,
		Permissions: []string{security.CapIPC},
		Description: "Returns whatever it is sent; the environment's hello-world application.",
	}
}

// ManifestJSON renders the manifest in the on-disk format.
func ManifestJSON() []byte {
	b, err := json.MarshalIndent(Manifest(), "", "  ")
	if err != nil {
		panic("echo: manifest cannot fail to marshal: " + err.Error())
	}
	return append(b, '\n')
}

// Echo is the application instance.
type Echo struct {
	log     *slog.Logger
	started time.Time

	mu    sync.Mutex
	count int64
}

// Factory builds an Echo instance.
func Factory(lc app.LaunchContext) (app.Instance, error) {
	return &Echo{
		log:     lc.Log,
		started: time.Now(),
	}, nil
}

// RegisterRoutes installs the application's IPC methods.
func (e *Echo) RegisterRoutes(r *ipc.Router, base string) error {
	if err := r.Handle(base+"/echo", e.handleEcho); err != nil {
		return err
	}
	return r.Handle(base+"/stats", e.handleStats)
}

// Run blocks until the process is stopped. A real application would run
// its event loop here; this one just waits to be told to stop.
func (e *Echo) Run(ctx context.Context, p *process.Process) error {
	e.log.Info("echo app running", "pid", p.ID())
	<-ctx.Done()
	e.log.Info("echo app stopping")
	return nil
}

func (e *Echo) handleEcho(ctx context.Context, req ipc.Request) (any, error) {
	var p struct {
		Msg string `json:"msg"`
	}
	if err := ipc.DecodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.Msg == "" {
		return nil, fmt.Errorf("params.msg is required")
	}
	e.mu.Lock()
	e.count++
	n := e.count
	e.mu.Unlock()
	return map[string]any{"msg": p.Msg, "echoes": n}, nil
}

func (e *Echo) handleStats(ctx context.Context, req ipc.Request) (any, error) {
	e.mu.Lock()
	n := e.count
	e.mu.Unlock()
	return map[string]any{
		"echoes":         n,
		"uptime_seconds": time.Since(e.started).Seconds(),
	}, nil
}
