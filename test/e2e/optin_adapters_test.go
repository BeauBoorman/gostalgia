package e2e

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gostalgia/internal/runtime"
	"gostalgia/internal/security"
	"gostalgia/platform"
)

type e2eMockNetAdapter struct{}

func (m *e2eMockNetAdapter) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return nil, nil
}

func (m *e2eMockNetAdapter) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"status":"online"}`)),
	}, nil
}

type e2eMockClipboardAdapter struct {
	text string
}

func (m *e2eMockClipboardAdapter) ReadText(ctx context.Context) (string, error) {
	return m.text, nil
}

func (m *e2eMockClipboardAdapter) WriteText(ctx context.Context, text string) error {
	m.text = text
	return nil
}

func (m *e2eMockClipboardAdapter) Available() bool {
	return true
}

func TestE2EOptInPlatformAdapters(t *testing.T) {
	// Set mock adapters for clipboard and network
	origClip := platform.GetHostClipboard()
	origNet := platform.GetNetworkAdapter()
	defer func() {
		platform.SetHostClipboard(origClip)
		platform.SetNetworkAdapter(origNet)
	}()

	mockClip := &e2eMockClipboardAdapter{text: "host-initial"}
	platform.SetHostClipboard(mockClip)
	platform.SetNetworkAdapter(&e2eMockNetAdapter{})

	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{
		Root: root,
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	defer rt.Shutdown("test complete")

	client := dialRunning(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Initial Clipboard: default policy is internal-only
	var clipOut struct {
		Text   string `json:"text"`
		Source string `json:"source"`
	}
	// Write to internal clipboard
	var writeOut struct {
		Written bool   `json:"written"`
		Source  string `json:"source"`
	}
	if err := client.Call(ctx, "clipboard/write", map[string]string{"text": "internal data"}, &writeOut); err != nil {
		t.Fatalf("clipboard/write: %v", err)
	}
	if writeOut.Source != "internal" {
		t.Fatalf("expected write source 'internal', got %q", writeOut.Source)
	}
	// Verify host clipboard untouched
	if mockClip.text != "host-initial" {
		t.Fatalf("expected mock host clipboard to be unchanged, got %q", mockClip.text)
	}

	// Read internal clipboard
	if err := client.Call(ctx, "clipboard/read", nil, &clipOut); err != nil {
		t.Fatalf("clipboard/read: %v", err)
	}
	if clipOut.Text != "internal data" || clipOut.Source != "internal" {
		t.Fatalf("unexpected clipboard read: %+v", clipOut)
	}

	// 2. Opt-in: Update policy via sys/policy/update to enable host clipboard, hostfs, and network
	sharedHostDir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(sharedHostDir, "readme.txt"), []byte("from host folder"), 0o644))

	newPolicy := security.OperatorPolicy{
		Clipboard: security.ClipboardPolicy{
			Enabled:        true,
			AllowHostRead:  true,
			AllowHostWrite: true,
		},
		HostFS: security.HostFSPolicy{
			Enabled:      true,
			AllowedPaths: []string{sharedHostDir},
			AllowRead:    true,
			AllowWrite:   true,
		},
		Network: security.NetworkPolicy{
			Enabled:      true,
			AllowedHosts: []string{"api.example.com"},
		},
	}
	var polUpdateOut struct {
		Updated bool `json:"updated"`
	}
	if err := client.Call(ctx, "sys/policy/update", newPolicy, &polUpdateOut); err != nil {
		t.Fatalf("sys/policy/update: %v", err)
	}
	if !polUpdateOut.Updated {
		t.Fatalf("policy was not updated")
	}

	// 3. Test Host Clipboard with OSC sanitization
	// Malicious text with OSC 52 sequence
	inputWithOSC := "clean message\x1b]52;c;c2VjcmV0\x07"
	if err := client.Call(ctx, "clipboard/write", map[string]string{"text": inputWithOSC}, &writeOut); err != nil {
		t.Fatalf("clipboard/write host: %v", err)
	}
	if mockClip.text != "clean message" {
		t.Fatalf("expected mock host clipboard to receive sanitized text, got %q", mockClip.text)
	}

	// 4. Test Shared Host Folder Mounting
	var mountOut struct {
		Mounted   bool   `json:"mounted"`
		MountPath string `json:"mount_path"`
	}
	if err := client.Call(ctx, "hostfs/mount", map[string]any{
		"host_path":  sharedHostDir,
		"mount_path": "/mnt/host",
		"read_only":  false,
	}, &mountOut); err != nil {
		t.Fatalf("hostfs/mount: %v", err)
	}
	if !mountOut.Mounted || mountOut.MountPath != "/mnt/host" {
		t.Fatalf("unexpected mount output: %+v", mountOut)
	}
	t.Cleanup(func() {
		var out struct {
			Unmounted bool `json:"unmounted"`
		}
		_ = client.Call(ctx, "hostfs/unmount", map[string]string{"mount_path": "/mnt/host"}, &out)
	})

	// Read file from shared mount via fs/read
	var readOut struct {
		DataBase64 string `json:"data_base64"`
	}
	if err := client.Call(ctx, "fs/read", map[string]string{"path": "/mnt/host/readme.txt"}, &readOut); err != nil {
		t.Fatalf("fs/read: %v", err)
	}
	dataBytes, _ := base64.StdEncoding.DecodeString(readOut.DataBase64)
	if string(dataBytes) != "from host folder" {
		t.Fatalf("expected 'from host folder', got %q", string(dataBytes))
	}

	// Write new file to shared mount via fs/write
	writePayload := base64.StdEncoding.EncodeToString([]byte("written via gostalgia vfs"))
	var fsWriteOut map[string]any
	if err := client.Call(ctx, "fs/write", map[string]any{
		"path":        "/mnt/host/output.txt",
		"data_base64": writePayload,
	}, &fsWriteOut); err != nil {
		t.Fatalf("fs/write to shared host: %v", err)
	}

	// Verify written to actual host disk
	hostDiskData, err := os.ReadFile(filepath.Join(sharedHostDir, "output.txt"))
	if err != nil {
		t.Fatalf("failed to read from host disk: %v", err)
	}
	if string(hostDiskData) != "written via gostalgia vfs" {
		t.Fatalf("expected 'written via gostalgia vfs', got %q", string(hostDiskData))
	}

	// 5. Test Network Egress via net/fetch
	var netOut struct {
		Status     int    `json:"status"`
		DataBase64 string `json:"data_base64"`
	}
	if err := client.Call(ctx, "net/fetch", map[string]any{
		"url": "https://api.example.com/status",
	}, &netOut); err != nil {
		t.Fatalf("net/fetch: %v", err)
	}
	if netOut.Status != 200 {
		t.Fatalf("expected HTTP 200, got %d", netOut.Status)
	}
	respBytes, _ := base64.StdEncoding.DecodeString(netOut.DataBase64)
	if string(respBytes) != `{"status":"online"}` {
		t.Fatalf("unexpected response body: %q", string(respBytes))
	}

	// 6. Test Unmount
	var unmountOut struct {
		Unmounted bool `json:"unmounted"`
	}
	if err := client.Call(ctx, "hostfs/unmount", map[string]string{"mount_path": "/mnt/host"}, &unmountOut); err != nil {
		t.Fatalf("hostfs/unmount: %v", err)
	}
	if !unmountOut.Unmounted {
		t.Fatalf("expected unmount to succeed")
	}
}
