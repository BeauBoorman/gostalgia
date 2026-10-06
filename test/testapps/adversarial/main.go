package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"gostalgia/sdk"
)

type adversarialApp struct{}

func (a *adversarialApp) Init(ctx *sdk.Context) error {
	_ = ctx.Handle("probe_network", func(c context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = sdk.DecodeParams(raw, &p)
		target := p.Target
		if target == "" {
			target = "1.1.1.1:80"
		}
		d := net.Dialer{Timeout: 1 * time.Second}
		conn, err := d.DialContext(c, "tcp", target)
		if err != nil {
			return map[string]any{
				"connected": false,
				"error":     err.Error(),
			}, nil
		}
		_ = conn.Close()
		return map[string]any{
			"connected": true,
		}, nil
	})

	_ = ctx.Handle("spawn_descendant", func(c context.Context, raw json.RawMessage) (any, error) {
		cmd := exec.CommandContext(c, os.Args[0], "--dummy")
		if err := cmd.Start(); err != nil {
			return map[string]any{
				"spawned": false,
				"error":   err.Error(),
			}, nil
		}
		// Allow descendant a moment to run work. If killed by supervisor or denied, Wait fails.
		done := make(chan error, 1)
		go func() {
			done <- cmd.Wait()
		}()
		select {
		case err := <-done:
			if err != nil {
				return map[string]any{
					"spawned": false,
					"error":   err.Error(),
				}, nil
			}
			return map[string]any{
				"spawned": true,
			}, nil
		case <-time.After(100 * time.Millisecond):
			// Still running after 100ms: process was not killed
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return map[string]any{
				"spawned": true,
			}, nil
		}
	})

	_ = ctx.Handle("probe_write", func(c context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Path string `json:"path"`
		}
		_ = sdk.DecodeParams(raw, &p)
		if p.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		err := os.WriteFile(p.Path, []byte("adversarial"), 0644)
		if err != nil {
			return map[string]any{
				"written": false,
				"error":   err.Error(),
			}, nil
		}
		_ = os.Remove(p.Path)
		return map[string]any{
			"written": true,
		}, nil
	})

	viewFn := func(c context.Context) (sdk.View, error) {
		return sdk.View{
			State: sdk.ViewReady,
			Title: "Adversarial App",
		}, nil
	}
	actionFn := func(c context.Context, r sdk.ActionRequest) (sdk.View, error) {
		return viewFn(c)
	}
	return ctx.Present(viewFn, actionFn)
}

func (a *adversarialApp) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (a *adversarialApp) Stop(ctx context.Context) error {
	return nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--dummy" {
		// Used by spawn_descendant test
		time.Sleep(500 * time.Millisecond)
		return
	}
	if err := sdk.Serve(&adversarialApp{}); err != nil {
		fmt.Fprintf(os.Stderr, "adversarial serve error: %v\n", err)
		os.Exit(1)
	}
}
