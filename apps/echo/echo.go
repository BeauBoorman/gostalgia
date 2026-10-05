// Package echo is the reference Gostalgia app. It imports only the public SDK
// and the standard library; copy its manifest, factory, and lifecycle pattern.
package echo

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gostalgia/sdk"
)

const ID = "com.gostalgia.echo"

//go:embed manifest.json
var manifestJSON []byte

// Manifest parses the embedded source of truth, never a duplicate Go literal.
func Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestJSON)
	if err != nil {
		panic(err) // invalid builtin data is a programming error
	}
	return m
}

func ManifestJSON() []byte { return append([]byte(nil), manifestJSON...) }

type Echo struct {
	app     *sdk.Context
	started time.Time
	mu      sync.Mutex
	count   int64
}

func Factory() (sdk.Instance, error) { return &Echo{}, nil }

// Init runs before routes become visible. Mutable state uses a mutex because
// handlers can run concurrently with each other and with Run.
func (e *Echo) Init(app *sdk.Context) error {
	e.app, e.started = app, time.Now()
	for _, route := range []struct {
		name string
		h    sdk.Handler
	}{{"echo", e.echo}, {"stats", e.stats}, {"identity", e.identity}} {
		if err := app.Handle(route.name, route.h); err != nil {
			return err
		}
	}
	return nil
}

func (e *Echo) Run(ctx context.Context) error {
	e.app.Log.Info("echo running")
	<-ctx.Done()
	return nil
}

// Stop is called once, including after failed Init. Echo owns no resources.
func (e *Echo) Stop(ctx context.Context) error { return nil }

func (e *Echo) echo(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Msg string `json:"msg"`
	}
	if err := sdk.DecodeParams(raw, &p); err != nil {
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

func (e *Echo) stats(ctx context.Context, raw json.RawMessage) (any, error) {
	e.mu.Lock()
	n := e.count
	e.mu.Unlock()
	return map[string]any{"echoes": n, "uptime_seconds": time.Since(e.started).Seconds()}, nil
}

// identity calls a system service through the SDK. Even an admin invoking this
// route sees only Echo's manifest grant, proving no caller privilege is borrowed.
func (e *Echo) identity(ctx context.Context, raw json.RawMessage) (any, error) {
	var out json.RawMessage
	if err := e.app.Call(ctx, "session/whoami", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
