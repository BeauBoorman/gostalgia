package e2e

import (
	"context"
	"testing"
	"time"

	"gostalgia/internal/runtime"
	"gostalgia/sdk"
)

func TestSettingsAndConfigE2E(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, Verbose: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { rt.Shutdown("test finished") })

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Check default config value
	var getResp struct {
		Path  string `json:"path"`
		Value any    `json:"value"`
		Found bool   `json:"found"`
	}
	if err := client.Call(ctx, "config/get", map[string]string{"path": "theme"}, &getResp); err != nil {
		t.Fatalf("config/get theme: %v", err)
	}
	if getResp.Value != "nostalgia" {
		t.Fatalf("default theme = %v, want 'nostalgia'", getResp.Value)
	}

	// 2. Set user config value
	var setResp map[string]any
	if err := client.Call(ctx, "config/set", map[string]any{
		"path":  "theme",
		"value": "midnight",
		"layer": "user",
	}, &setResp); err != nil {
		t.Fatalf("config/set theme: %v", err)
	}

	// 3. Verify get returns midnight
	if err := client.Call(ctx, "config/get", map[string]string{"path": "theme"}, &getResp); err != nil {
		t.Fatalf("config/get theme after set: %v", err)
	}
	if getResp.Value != "midnight" {
		t.Fatalf("updated theme = %v, want 'midnight'", getResp.Value)
	}

	// 4. Verify config/explain
	var explanation struct {
		Path      string `json:"path"`
		Effective any    `json:"effective"`
		Winner    string `json:"winner"`
	}
	if err := client.Call(ctx, "config/explain", map[string]string{"path": "theme"}, &explanation); err != nil {
		t.Fatalf("config/explain theme: %v", err)
	}
	if explanation.Winner != "user" || explanation.Effective != "midnight" {
		t.Fatalf("unexpected explanation: %+v", explanation)
	}

	// 5. Preview theme "monochrome"
	var previewResp map[string]any
	if err := client.Call(ctx, "config/preview", map[string]any{
		"path":  "theme",
		"value": "monochrome",
	}, &previewResp); err != nil {
		t.Fatalf("config/preview theme: %v", err)
	}
	if err := client.Call(ctx, "config/get", map[string]string{"path": "theme"}, &getResp); err != nil {
		t.Fatalf("config/get theme in preview: %v", err)
	}
	if getResp.Value != "monochrome" {
		t.Fatalf("theme in preview = %v, want 'monochrome'", getResp.Value)
	}

	// 6. Cancel preview - rolls back to midnight
	if err := client.Call(ctx, "config/cancel_preview", nil, &previewResp); err != nil {
		t.Fatalf("config/cancel_preview: %v", err)
	}
	if err := client.Call(ctx, "config/get", map[string]string{"path": "theme"}, &getResp); err != nil {
		t.Fatalf("config/get theme after cancel preview: %v", err)
	}
	if getResp.Value != "midnight" {
		t.Fatalf("theme after cancel preview = %v, want 'midnight'", getResp.Value)
	}

	// 7. Launch Settings app and interact via presentation contract
	var launchResp struct {
		PID int `json:"pid"`
	}
	if err := client.Call(ctx, "app/launch", map[string]string{"id": "com.gostalgia.settings"}, &launchResp); err != nil {
		t.Fatalf("app/launch settings: %v", err)
	}

	var view sdk.View
	if err := client.Call(ctx, "app/com.gostalgia.settings/view", sdk.ViewRequest{Version: 1}, &view); err != nil {
		t.Fatalf("app/com.gostalgia.settings/view: %v", err)
	}
	if view.Title == "" || len(view.Items) == 0 {
		t.Fatalf("settings view empty or invalid: %+v", view)
	}

	// Action: switch category to accessibility
	var actionResp sdk.View
	req := sdk.ActionRequest{
		Version:   1,
		RequestID: "req-1",
		Action:    "cat_accessibility",
		Instance:  view.Instance,
	}
	if err := client.Call(ctx, "app/com.gostalgia.settings/action", req, &actionResp); err != nil {
		t.Fatalf("settings action cat_accessibility: %v", err)
	}
	if actionResp.Title != "Settings - Accessibility & Motion" {
		t.Fatalf("unexpected settings title after switching category: %q", actionResp.Title)
	}

	// Stop Settings app
	if err := client.Call(ctx, "app/stop", map[string]string{"id": "com.gostalgia.settings"}, nil); err != nil {
		t.Fatalf("app/stop settings: %v", err)
	}
}
