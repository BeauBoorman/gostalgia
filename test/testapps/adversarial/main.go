package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gostalgia/sdk"
)

type adversarialApp struct {
	mu           sync.Mutex
	hangOnStop   bool
	hangDuration time.Duration
}

func (a *adversarialApp) Init(ctx *sdk.Context) error {
	// 1. Host Network Probe
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

	// 2. Descendant Process Spawn Probe
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
			<-done
			return map[string]any{
				"spawned": true,
			}, nil
		}
	})

	// 3. Host Filesystem Write Probe
	_ = ctx.Handle("probe_write", func(c context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Path string `json:"path"`
			Data string `json:"data"`
		}
		_ = sdk.DecodeParams(raw, &p)
		if p.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		data := []byte(p.Data)
		if len(data) == 0 {
			data = []byte("adversarial")
		}
		err := os.WriteFile(p.Path, data, 0644)
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

	// 4. Host Filesystem Read Probe
	_ = ctx.Handle("probe_read", func(c context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Path string `json:"path"`
		}
		_ = sdk.DecodeParams(raw, &p)
		if p.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		data, err := os.ReadFile(p.Path)
		if err != nil {
			return map[string]any{
				"read":  false,
				"error": err.Error(),
			}, nil
		}
		return map[string]any{
			"read":    true,
			"content": string(data),
		}, nil
	})

	// 5. VFS Traversal and Escape Probe via SDK Call
	_ = ctx.Handle("probe_vfs_escape", func(c context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Path string `json:"path"`
		}
		_ = sdk.DecodeParams(raw, &p)
		var out any
		err := ctx.Call(c, "fs/read", map[string]string{"path": p.Path}, &out)
		if err != nil {
			return map[string]any{
				"escaped": false,
				"error":   err.Error(),
			}, nil
		}
		return map[string]any{
			"escaped": true,
			"data":    out,
		}, nil
	})

	// 6. Credential Leakage Probe
	_ = ctx.Handle("probe_credentials", func(c context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			RuntimeJSONPath string `json:"runtime_json_path"`
		}
		_ = sdk.DecodeParams(raw, &p)

		envVars := os.Environ()
		leakedOperator := false
		hasAppToken := false
		for _, env := range envVars {
			k, _, _ := strings.Cut(env, "=")
			kUpper := strings.ToUpper(k)
			if strings.Contains(kUpper, "OPERATOR") {
				leakedOperator = true
			}
			if kUpper == "GOSTALGIA_APP_TOKEN" {
				hasAppToken = true
			}
		}

		runtimeRead := false
		var runtimeErr string
		if p.RuntimeJSONPath != "" {
			if b, err := os.ReadFile(p.RuntimeJSONPath); err == nil && len(b) > 0 {
				runtimeRead = true
			} else if err != nil {
				runtimeErr = err.Error()
			}
		}

		return map[string]any{
			"leaked_operator_token": leakedOperator,
			"has_app_token":         hasAppToken,
			"runtime_json_read":     runtimeRead,
			"runtime_json_error":    runtimeErr,
			"env_count":             len(envVars),
		}, nil
	})

	// 7. Child Environment Sanitization Probe
	_ = ctx.Handle("probe_child_env", func(c context.Context, raw json.RawMessage) (any, error) {
		cmd := exec.CommandContext(c, os.Args[0], "--print-env")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return map[string]any{
				"success": false,
				"error":   err.Error(),
			}, nil
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		return map[string]any{
			"success": true,
			"environ": lines,
		}, nil
	})

	// 8. Permission Borrowing Probe (attempting privileged calls)
	_ = ctx.Handle("borrow_permissions", func(c context.Context, raw json.RawMessage) (any, error) {
		var whoami any
		errWhoami := ctx.Call(c, "session/whoami", nil, &whoami)

		var shutdownRes any
		errShutdown := ctx.Call(c, "sys/shutdown", nil, &shutdownRes)

		var procStopRes any
		errProcStop := ctx.Call(c, "proc/stop", map[string]any{"id": 1}, &procStopRes)

		var fsGrantRes any
		errFsGrant := ctx.Call(c, "fs/grant", map[string]any{
			"app_id": ctx.Manifest.ID,
			"path":   "/users/guest",
			"access": "read-write",
		}, &fsGrantRes)

		var appLaunchRes any
		errAppLaunch := ctx.Call(c, "app/launch", map[string]string{"id": "com.gostalgia.echo"}, &appLaunchRes)

		errMap := make(map[string]string)
		if errShutdown != nil {
			errMap["shutdown"] = errShutdown.Error()
		}
		if errProcStop != nil {
			errMap["proc_stop"] = errProcStop.Error()
		}
		if errFsGrant != nil {
			errMap["fs_grant"] = errFsGrant.Error()
		}
		if errAppLaunch != nil {
			errMap["app_launch"] = errAppLaunch.Error()
		}
		if errWhoami != nil {
			errMap["whoami"] = errWhoami.Error()
		}

		return map[string]any{
			"shutdown_allowed":   errShutdown == nil,
			"proc_stop_allowed":  errProcStop == nil,
			"fs_grant_allowed":   errFsGrant == nil,
			"app_launch_allowed": errAppLaunch == nil,
			"whoami":             whoami,
			"errors":             errMap,
		}, nil
	})

	// 9. CPU Exhaustion Probe
	_ = ctx.Handle("burn_cpu", func(c context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			DurationMS int `json:"duration_ms"`
			Workers    int `json:"workers"`
		}
		_ = sdk.DecodeParams(raw, &p)
		if p.DurationMS <= 0 {
			p.DurationMS = 100
		}
		if p.Workers <= 0 {
			p.Workers = 2
		}

		stop := make(chan struct{})
		var ops atomic.Uint64
		var wg sync.WaitGroup
		for i := 0; i < p.Workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				local := uint64(0)
				for {
					select {
					case <-stop:
						ops.Add(local)
						return
					default:
						local++
					}
				}
			}()
		}
		time.Sleep(time.Duration(p.DurationMS) * time.Millisecond)
		close(stop)
		wg.Wait()

		return map[string]any{
			"completed":   true,
			"duration_ms": p.DurationMS,
			"operations":  ops.Load(),
		}, nil
	})

	// 10. Memory Exhaustion Probe
	_ = ctx.Handle("consume_memory", func(c context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Megabytes int `json:"megabytes"`
		}
		_ = sdk.DecodeParams(raw, &p)
		if p.Megabytes <= 0 {
			p.Megabytes = 64
		}

		chunks := make([][]byte, 0, p.Megabytes)
		allocated := 0
		var allocErr string
		for i := 0; i < p.Megabytes; i++ {
			chunk := make([]byte, 1024*1024)
			// Touch memory to commit pages
			for j := 0; j < len(chunk); j += 4096 {
				chunk[j] = 0xAA
			}
			chunks = append(chunks, chunk)
			allocated++
		}

		return map[string]any{
			"allocated_mb": allocated,
			"error":        allocErr,
		}, nil
	})

	// 11. Uncooperative Shutdown Probe
	_ = ctx.Handle("hang_on_stop", func(c context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Hang       bool `json:"hang"`
			DurationMS int  `json:"duration_ms"`
		}
		_ = sdk.DecodeParams(raw, &p)
		a.mu.Lock()
		a.hangOnStop = p.Hang
		if p.DurationMS <= 0 {
			a.hangDuration = 30 * time.Second
		} else {
			a.hangDuration = time.Duration(p.DurationMS) * time.Millisecond
		}
		a.mu.Unlock()

		return map[string]any{
			"hang_configured": p.Hang,
			"duration_ms":     p.DurationMS,
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
	a.mu.Lock()
	hang := a.hangOnStop
	dur := a.hangDuration
	a.mu.Unlock()

	if hang {
		// Intentionally ignore ctx cancellation and sleep
		time.Sleep(dur)
	}
	return nil
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--dummy":
			// Used by spawn_descendant test
			time.Sleep(2 * time.Second)
			return
		case "--print-env":
			for _, env := range os.Environ() {
				fmt.Println(env)
			}
			return
		}
	}
	if err := sdk.Serve(&adversarialApp{}); err != nil {
		fmt.Fprintf(os.Stderr, "adversarial serve error: %v\n", err)
		os.Exit(1)
	}
}
