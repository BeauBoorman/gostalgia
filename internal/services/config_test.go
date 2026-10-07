package services

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"gostalgia/internal/config"
	"gostalgia/internal/events"
	"gostalgia/internal/ipc"
	"gostalgia/internal/security"
	"gostalgia/internal/service"
)

type configTestEnv struct {
	t      *testing.T
	svc    *ConfigService
	router *ipc.Router
	bus    *events.Bus
	store  *config.LayeredStore
}

func newTestConfigService(t *testing.T) *configTestEnv {
	t.Helper()
	dir := t.TempDir()
	sysPath := filepath.Join(dir, "system.json")
	userPath := filepath.Join(dir, "user.json")

	store, err := config.NewLayeredStore(sysPath, userPath)
	if err != nil {
		t.Fatal(err)
	}

	router := ipc.NewRouter()
	bus := events.NewBus()
	svcCtx := &service.Context{
		Layered: store,
		Config:  store.SystemStore(),
		Router:  router,
		Events:  bus,
	}

	svc := NewConfig()
	if err := svc.Init(svcCtx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = svc.Stop(context.Background())
	})

	return &configTestEnv{
		t:      t,
		svc:    svc,
		router: router,
		bus:    bus,
		store:  store,
	}
}

func (e *configTestEnv) call(ctx context.Context, caps *security.Capabilities, method string, params any) ipc.Response {
	e.t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			e.t.Fatal(err)
		}
		raw = b
	}
	if caps != nil {
		ctx = ipc.WithCapabilities(ctx, caps)
	}
	return e.router.Dispatch(ctx, ipc.Request{ID: 1, Method: method, Params: raw})
}

func (e *configTestEnv) callAs(ctx context.Context, principal security.Principal, caps *security.Capabilities, method string, params any) ipc.Response {
	e.t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			e.t.Fatal(err)
		}
		raw = b
	}
	if caps != nil {
		ctx = ipc.WithCapabilities(ctx, caps)
	}
	ctx = ipc.WithPrincipal(ctx, principal)
	return e.router.Dispatch(ctx, ipc.Request{ID: 1, Method: method, Params: raw})
}

func TestConfigServiceGetAndSet(t *testing.T) {
	env := newTestConfigService(t)

	// 1. Get default value
	resp := env.call(context.Background(), security.AdminCapabilities(), "config/get", map[string]any{"path": "theme"})
	if !resp.OK {
		t.Fatalf("config/get failed: %s", resp.Error)
	}
	var getOut getResp
	if err := json.Unmarshal(resp.Data, &getOut); err != nil {
		t.Fatal(err)
	}
	if getOut.Value != "nostalgia" || getOut.Layer != config.LayerDefault {
		t.Fatalf("expected default nostalgia, got %+v", getOut)
	}

	// 2. Set user value
	setResp := env.call(context.Background(), security.AdminCapabilities(), "config/set", map[string]any{"path": "theme", "value": "midnight"})
	if !setResp.OK {
		t.Fatalf("config/set failed: %s", setResp.Error)
	}

	// 3. Get updated effective value
	resp = env.call(context.Background(), security.AdminCapabilities(), "config/get", map[string]any{"path": "theme"})
	if !resp.OK {
		t.Fatalf("config/get failed: %s", resp.Error)
	}
	if err := json.Unmarshal(resp.Data, &getOut); err != nil {
		t.Fatal(err)
	}
	if getOut.Value != "midnight" || getOut.Layer != config.LayerUser {
		t.Fatalf("expected user midnight, got %+v", getOut)
	}
}

func TestConfigServicePermissions(t *testing.T) {
	env := newTestConfigService(t)
	appPrincipal := security.AppPrincipal("com.test.app", 100, "sess-1", security.User{ID: "u-1", Name: "appuser"})

	// App without CapConfigWrite cannot set user layer
	resp := env.callAs(context.Background(), appPrincipal, security.NewCapabilities(security.CapIPC), "config/set", map[string]any{"path": "theme", "value": "monochrome", "layer": "user"})
	if resp.OK {
		t.Fatal("expected set user layer to fail without config.write capability")
	}

	// App with CapConfigWrite CAN set user layer
	resp = env.callAs(context.Background(), appPrincipal, security.NewCapabilities(security.CapIPC, security.CapConfigWrite), "config/set", map[string]any{"path": "theme", "value": "monochrome", "layer": "user"})
	if !resp.OK {
		t.Fatalf("expected set user layer to succeed with config.write: %s", resp.Error)
	}

	// App cannot set system layer even with CapConfigWrite (requires admin)
	resp = env.callAs(context.Background(), appPrincipal, security.NewCapabilities(security.CapIPC, security.CapConfigWrite), "config/set", map[string]any{"path": "theme", "value": "high-contrast", "layer": "system"})
	if resp.OK {
		t.Fatal("expected set system layer to fail for non-admin app")
	}

	// Admin can set system layer
	resp = env.call(context.Background(), security.AdminCapabilities(), "config/set", map[string]any{"path": "theme", "value": "high-contrast", "layer": "system"})
	if !resp.OK {
		t.Fatalf("expected admin to set system layer: %s", resp.Error)
	}
}

// #77: config read routes must require config.read, not baseline ipc.
func TestConfigServiceReadRequiresCapability(t *testing.T) {
	env := newTestConfigService(t)
	app := security.AppPrincipal("com.test.app", 100, "sess-1", security.User{ID: "u-1", Name: "appuser"})
	ipcOnly := security.NewCapabilities(security.CapIPC)

	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"config/get", map[string]any{"path": "theme"}},
		{"config/list", nil},
		{"config/snapshot", nil},
		{"config/explain", map[string]any{"path": "theme"}},
		{"config/preview", map[string]any{"path": "theme", "value": "midnight"}},
		{"config/validate", map[string]any{"path": "theme", "value": "midnight"}},
		{"config/cancel_preview", nil},
	} {
		resp := env.callAs(context.Background(), app, ipcOnly, tc.method, tc.params)
		if resp.OK {
			t.Fatalf("%s accepted an app without config.read", tc.method)
		}
	}

	// The dedicated capability unlocks the read routes.
	readCaps := security.NewCapabilities(security.CapIPC, security.CapConfigRead)
	for _, method := range []string{"config/get", "config/list", "config/explain", "config/validate"} {
		resp := env.callAs(context.Background(), app, readCaps, method, map[string]any{"path": "theme", "value": "midnight"})
		if !resp.OK {
			t.Fatalf("%s rejected an app holding config.read: %s", method, resp.Error)
		}
	}
}

// #78: caller-supplied app_id on read routes must be pinned to the caller,
// matching config/set — otherwise any app reads another app's app layer.
func TestConfigServiceAppIDPinningOnReads(t *testing.T) {
	env := newTestConfigService(t)
	evil := security.AppPrincipal("com.test.evil", 101, "sess-1", security.User{ID: "u-1", Name: "appuser"})
	readCaps := security.NewCapabilities(security.CapIPC, security.CapConfigRead)

	// Seed a secret in the victim's app layer.
	resp := env.call(context.Background(), security.AdminCapabilities(), "config/set",
		map[string]any{"path": "token", "value": "victim-secret", "layer": "app", "app_id": "com.victim.app"})
	if !resp.OK {
		t.Fatalf("seeding victim app layer failed: %s", resp.Error)
	}

	// An app may not pass another app's id on any read route.
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"config/get", map[string]any{"path": "token", "app_id": "com.victim.app"}},
		{"config/list", map[string]any{"app_id": "com.victim.app"}},
		{"config/explain", map[string]any{"path": "token", "app_id": "com.victim.app"}},
	} {
		if resp := env.callAs(context.Background(), evil, readCaps, tc.method, tc.params); resp.OK {
			t.Fatalf("%s accepted a foreign app_id", tc.method)
		}
	}

	// Reads without app_id resolve to the caller's own layer, not the victim's.
	resp = env.callAs(context.Background(), evil, readCaps, "config/get", map[string]any{"path": "token"})
	if !resp.OK {
		t.Fatalf("config/get failed: %s", resp.Error)
	}
	var getOut getResp
	must(t, json.Unmarshal(resp.Data, &getOut))
	if getOut.Found {
		t.Fatal("app read resolved a value from another app's layer")
	}

	// Operators can still query a specific app layer.
	resp = env.call(context.Background(), security.AdminCapabilities(), "config/get",
		map[string]any{"path": "token", "layer": "app", "app_id": "com.victim.app"})
	if !resp.OK {
		t.Fatalf("operator config/get failed: %s", resp.Error)
	}
	must(t, json.Unmarshal(resp.Data, &getOut))
	if !getOut.Found || getOut.Value != "victim-secret" {
		t.Fatalf("operator read of victim app layer = %+v", getOut)
	}
}

func TestConfigServiceValidationAndConflicts(t *testing.T) {
	env := newTestConfigService(t)

	// Invalid theme rejected
	resp := env.call(context.Background(), security.AdminCapabilities(), "config/set", map[string]any{"path": "theme", "value": "neon_rainbow"})
	if resp.OK {
		t.Fatal("expected invalid theme to be rejected")
	}

	// Keybinding conflict rejected
	resp = env.call(context.Background(), security.AdminCapabilities(), "config/set", map[string]any{"path": "shortcuts.settings", "value": "f1"})
	if resp.OK {
		t.Fatal("expected keybinding conflict to be rejected")
	}

	// Validate endpoint detects conflicts in batch
	resp = env.call(context.Background(), security.AdminCapabilities(), "config/validate", map[string]any{
		"settings": map[string]any{
			"shortcuts": map[string]any{
				"home":     "f1",
				"settings": "f1",
			},
		},
	})
	if !resp.OK {
		t.Fatalf("validate call failed: %s", resp.Error)
	}
	var valOut validateResp
	if err := json.Unmarshal(resp.Data, &valOut); err != nil {
		t.Fatal(err)
	}
	if valOut.Valid || len(valOut.Conflicts) != 1 {
		t.Fatalf("expected validation failure with 1 conflict: %+v", valOut)
	}
}

func TestConfigServicePreviewLifecycle(t *testing.T) {
	env := newTestConfigService(t)

	var receivedEvents []config.ChangeEvent
	env.bus.Subscribe("config.changed", func(e events.Envelope) {
		if ev, ok := e.Payload.(config.ChangeEvent); ok {
			receivedEvents = append(receivedEvents, ev)
		}
	})

	// 1. Preview theme
	resp := env.call(context.Background(), security.AdminCapabilities(), "config/preview", map[string]any{"path": "theme", "value": "midnight"})
	if !resp.OK {
		t.Fatalf("preview failed: %s", resp.Error)
	}

	// Verify effective value is midnight
	resp = env.call(context.Background(), security.AdminCapabilities(), "config/get", map[string]any{"path": "theme"})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	var getOut getResp
	_ = json.Unmarshal(resp.Data, &getOut)
	if getOut.Value != "midnight" || getOut.Layer != config.LayerPreview {
		t.Fatalf("expected preview value midnight, got %+v", getOut)
	}

	// 2. Cancel preview
	resp = env.call(context.Background(), security.AdminCapabilities(), "config/cancel_preview", nil)
	if !resp.OK {
		t.Fatalf("cancel preview failed: %s", resp.Error)
	}

	// Verify reverted to default nostalgia
	resp = env.call(context.Background(), security.AdminCapabilities(), "config/get", map[string]any{"path": "theme"})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	_ = json.Unmarshal(resp.Data, &getOut)
	if getOut.Value != "nostalgia" || getOut.Layer != config.LayerDefault {
		t.Fatalf("expected reverted value nostalgia, got %+v", getOut)
	}

	// 3. Preview and commit
	_ = env.call(context.Background(), security.AdminCapabilities(), "config/preview", map[string]any{"path": "theme", "value": "monochrome"})
	resp = env.call(context.Background(), security.AdminCapabilities(), "config/commit_preview", map[string]any{"layer": "user"})
	if !resp.OK {
		t.Fatalf("commit preview failed: %s", resp.Error)
	}

	resp = env.call(context.Background(), security.AdminCapabilities(), "config/get", map[string]any{"path": "theme"})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	_ = json.Unmarshal(resp.Data, &getOut)
	if getOut.Value != "monochrome" || getOut.Layer != config.LayerUser {
		t.Fatalf("expected committed user value monochrome, got %+v", getOut)
	}

	// Check that events were emitted
	if len(receivedEvents) == 0 {
		t.Fatal("expected change events to be published to bus")
	}
}

func TestConfigServiceExplainAndList(t *testing.T) {
	env := newTestConfigService(t)

	// Explain default
	resp := env.call(context.Background(), security.AdminCapabilities(), "config/explain", map[string]any{"path": "theme"})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	var exp config.LayerExplanation
	if err := json.Unmarshal(resp.Data, &exp); err != nil {
		t.Fatal(err)
	}
	if exp.Winner != config.LayerDefault || exp.Effective != "nostalgia" {
		t.Fatalf("unexpected explanation: %+v", exp)
	}

	// List
	resp = env.call(context.Background(), security.AdminCapabilities(), "config/list", nil)
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	var listOut listResp
	if err := json.Unmarshal(resp.Data, &listOut); err != nil {
		t.Fatal(err)
	}
	if listOut.Effective["theme"] != "nostalgia" {
		t.Fatalf("effective theme in list = %v, want nostalgia", listOut.Effective["theme"])
	}
}
