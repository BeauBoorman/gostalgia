package services

import (
	"context"
	"encoding/json"
	"testing"

	"gostalgia/internal/security"
	"gostalgia/platform"
)

type mockClipboardAdapter struct {
	text string
	err  error
}

func (m *mockClipboardAdapter) ReadText(ctx context.Context) (string, error) {
	return m.text, m.err
}

func (m *mockClipboardAdapter) WriteText(ctx context.Context, text string) error {
	m.text = text
	return m.err
}

func (m *mockClipboardAdapter) Available() bool {
	return true
}

func TestClipboardService_InternalFallback(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Default policy has Clipboard.Enabled = false (internal fallback)
	capsNone := security.NewCapabilities()
	capsRead := security.NewCapabilities(security.CapClipboardRead)
	capsWrite := security.NewCapabilities(security.CapClipboardWrite)
	capsAll := security.NewCapabilities(security.CapIPC, security.CapClipboardRead, security.CapClipboardWrite)

	// 1. Write without capability fails
	res := env.call(ctx, capsNone, "clipboard/write", map[string]string{"text": "secret"})
	if res.OK {
		t.Fatal("expected clipboard/write without capability to fail")
	}

	// 2. Read without capability fails
	res = env.call(ctx, capsNone, "clipboard/read", nil)
	if res.OK {
		t.Fatal("expected clipboard/read without capability to fail")
	}

	// 3. Write with write capability succeeds to internal clipboard
	res = env.call(ctx, capsWrite, "clipboard/write", map[string]string{"text": "hello internal"})
	if !res.OK {
		t.Fatalf("clipboard/write failed: %s", res.Error)
	}

	// 4. Read with read capability returns internal text
	res = env.call(ctx, capsRead, "clipboard/read", nil)
	if !res.OK {
		t.Fatalf("clipboard/read failed: %s", res.Error)
	}
	var readData struct {
		Text   string `json:"text"`
		Source string `json:"source"`
	}
	must(t, json.Unmarshal(res.Data, &readData))
	if readData.Text != "hello internal" {
		t.Fatalf("expected 'hello internal', got %q", readData.Text)
	}
	if readData.Source != "internal" {
		t.Fatalf("expected source 'internal', got %q", readData.Source)
	}

	// 5. Status
	res = env.call(ctx, capsAll, "clipboard/status", nil)
	if !res.OK {
		t.Fatalf("clipboard/status failed: %s", res.Error)
	}
	var statusData struct {
		PolicyEnabled bool `json:"policy_enabled"`
		ContentBytes  int  `json:"content_bytes"`
	}
	must(t, json.Unmarshal(res.Data, &statusData))
	if statusData.PolicyEnabled {
		t.Fatal("expected policy_enabled to be false by default")
	}
	if statusData.ContentBytes != len("hello internal") {
		t.Fatalf("expected content_bytes %d, got %d", len("hello internal"), statusData.ContentBytes)
	}

	// 6. Clear
	res = env.call(ctx, capsWrite, "clipboard/clear", nil)
	if !res.OK {
		t.Fatalf("clipboard/clear failed: %s", res.Error)
	}
	res = env.call(ctx, capsRead, "clipboard/read", nil)
	if !res.OK {
		t.Fatalf("clipboard/read after clear failed: %s", res.Error)
	}
	must(t, json.Unmarshal(res.Data, &readData))
	if readData.Text != "" {
		t.Fatalf("expected empty text after clear, got %q", readData.Text)
	}
}

func TestClipboardService_HostIntegration(t *testing.T) {
	origAdapter := platform.GetHostClipboard()
	defer platform.SetHostClipboard(origAdapter)

	mock := &mockClipboardAdapter{text: "host initial"}
	platform.SetHostClipboard(mock)

	env := newTestEnv(t)
	ctx := context.Background()
	capsAll := security.NewCapabilities(security.CapClipboardRead, security.CapClipboardWrite)

	// Enable host clipboard in policy
	policy := env.ctx.Policy.Get()
	policy.Clipboard.Enabled = true
	policy.Clipboard.AllowHostRead = true
	policy.Clipboard.AllowHostWrite = true
	env.ctx.Policy.Set(policy)

	// 1. Read should fetch from host adapter
	res := env.call(ctx, capsAll, "clipboard/read", nil)
	if !res.OK {
		t.Fatalf("clipboard/read failed: %s", res.Error)
	}
	var readData struct {
		Text   string `json:"text"`
		Source string `json:"source"`
	}
	must(t, json.Unmarshal(res.Data, &readData))
	if readData.Text != "host initial" {
		t.Fatalf("expected 'host initial', got %q", readData.Text)
	}
	if readData.Source != "host" {
		t.Fatalf("expected source 'host', got %q", readData.Source)
	}

	// 2. Write should write to both host and internal
	res = env.call(ctx, capsAll, "clipboard/write", map[string]string{
		"text": "new host text\x1b]52;c;malicious\x07", // includes OSC 52 sequence!
	})
	if !res.OK {
		t.Fatalf("clipboard/write failed: %s", res.Error)
	}

	// Verify sanitized in mock adapter
	if mock.text != "new host text" {
		t.Fatalf("expected sanitized 'new host text' in host clipboard, got %q", mock.text)
	}

	// 3. Test AllowHostWrite = false
	policy.Clipboard.AllowHostWrite = false
	env.ctx.Policy.Set(policy)

	res = env.call(ctx, capsAll, "clipboard/write", map[string]string{
		"text": "only internal text",
	})
	if !res.OK {
		t.Fatalf("clipboard/write failed: %s", res.Error)
	}
	// Host adapter should not have changed
	if mock.text != "new host text" {
		t.Fatalf("expected host clipboard to remain unchanged, got %q", mock.text)
	}

	// 4. Test AllowHostRead = false
	policy.Clipboard.AllowHostRead = false
	env.ctx.Policy.Set(policy)

	res = env.call(ctx, capsAll, "clipboard/read", nil)
	if !res.OK {
		t.Fatalf("clipboard/read failed: %s", res.Error)
	}
	must(t, json.Unmarshal(res.Data, &readData))
	if readData.Source != "internal" {
		t.Fatalf("expected source 'internal' when CanReadHost=false, got %q", readData.Source)
	}
	if readData.Text != "only internal text" {
		t.Fatalf("expected 'only internal text', got %q", readData.Text)
	}
}
