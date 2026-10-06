package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gostalgia/internal/ipc"
	"gostalgia/internal/runtime"
	"gostalgia/platform"
)

func TestAppIdentityAndCredentialLifecycle(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, Verbose: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	defer rt.Shutdown("test end")

	// 1. Verify runtime.json contains operator token and no app tokens.
	data, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if err != nil {
		t.Fatalf("read runtime.json: %v", err)
	}
	var rInfo struct {
		Endpoint string `json:"endpoint"`
		Token    string `json:"token"`
	}
	if err := json.Unmarshal(data, &rInfo); err != nil {
		t.Fatal(err)
	}
	opToken := rInfo.Token

	// Verify app token for launched echo app exists in AppManager.
	appToken, ok := rt.Apps.AppToken("com.gostalgia.echo")
	if !ok || appToken == "" {
		t.Fatalf("expected active app token for echo app, got %q (ok=%v)", appToken, ok)
	}
	if appToken == opToken {
		t.Fatal("app token must be distinct from operator token")
	}

	// Verify app token is NOT in runtime.json or public process list.
	if strings.Contains(string(data), appToken) {
		t.Fatal("runtime.json must not leak app token")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 2. Connect as Operator: verify operator identity and admin authority.
	opConn, err := platform.DialIPC(rInfo.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	opClient, err := ipc.NewClient(opConn, opToken)
	if err != nil {
		t.Fatalf("operator dial: %v", err)
	}
	defer opClient.Close()

	var opWhoami struct {
		Principal string   `json:"principal"`
		Caps      []string `json:"capabilities"`
	}
	if err := opClient.Call(ctx, "session/whoami", nil, &opWhoami); err != nil {
		t.Fatalf("operator whoami: %v", err)
	}
	if opWhoami.Principal != "operator" {
		t.Errorf("operator principal = %q, want operator", opWhoami.Principal)
	}

	// Verify proc/list does not leak tokens.
	var procs []map[string]any
	if err := opClient.Call(ctx, "proc/list", nil, &procs); err != nil {
		t.Fatalf("proc/list: %v", err)
	}
	procsJSON, _ := json.Marshal(procs)
	if strings.Contains(string(procsJSON), appToken) || strings.Contains(string(procsJSON), opToken) {
		t.Fatal("proc/list must not leak tokens")
	}

	// 3. Connect as App: dial socket using appToken.
	appConn, err := platform.DialIPC(rInfo.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	appClient, err := ipc.NewClient(appConn, appToken)
	if err != nil {
		t.Fatalf("app dial: %v", err)
	}
	defer appClient.Close()

	var appWhoami struct {
		Principal string   `json:"principal"`
		AppID     string   `json:"app_id"`
		ProcessID int32    `json:"process_id"`
		Caps      []string `json:"capabilities"`
	}
	if err := appClient.Call(ctx, "session/whoami", nil, &appWhoami); err != nil {
		t.Fatalf("app whoami: %v", err)
	}
	if appWhoami.Principal != "app" {
		t.Errorf("app principal = %q, want app", appWhoami.Principal)
	}
	if appWhoami.AppID != "com.gostalgia.echo" {
		t.Errorf("app_id = %q, want com.gostalgia.echo", appWhoami.AppID)
	}

	// 4. Deny path: app attempts privileged methods it lacks grants for.
	// Echo only has "ipc" capability; lacks "shutdown", "proc.stop", "fs.write".
	if err := appClient.Call(ctx, "sys/shutdown", nil, nil); err == nil {
		t.Fatal("app call to sys/shutdown succeeded, want permission denied")
	}
	if err := appClient.Call(ctx, "proc/stop", map[string]any{"id": 1}, nil); err == nil {
		t.Fatal("app call to proc/stop succeeded, want permission denied")
	}
	if err := appClient.Call(ctx, "fs/write", map[string]string{"path": "/tmp/test", "data_base64": ""}, nil); err == nil {
		t.Fatal("app call to fs/write succeeded, want permission denied")
	}

	// Permitted call: app calls echo service (requires "ipc").
	var echoResp struct {
		Msg string `json:"msg"`
	}
	if err := appClient.Call(ctx, "app/com.gostalgia.echo/echo", map[string]string{"msg": "app-to-app"}, &echoResp); err != nil {
		t.Fatalf("app call to echo failed: %v", err)
	}
	if echoResp.Msg != "app-to-app" {
		t.Errorf("echoResp.Msg = %q, want app-to-app", echoResp.Msg)
	}

	// 5. App stop revokes authority; stale credentials fail.
	if err := rt.Apps.Stop("com.gostalgia.echo", 5*time.Second); err != nil {
		t.Fatalf("stop app: %v", err)
	}

	// Subsequent call on existing connection must fail as revoked.
	if err := appClient.Call(ctx, "session/whoami", nil, nil); err == nil {
		t.Fatal("call on revoked credential succeeded, want error")
	}

	// Replay attempt: dialing a new connection with stale/revoked appToken must fail handshake.
	staleConn, err := platform.DialIPC(rInfo.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer staleConn.Close()
	if _, err := ipc.NewClient(staleConn, appToken); err == nil {
		t.Fatal("handshake with revoked app token succeeded, want error")
	}
}

func TestConfusedDeputyViaEchoIdentity(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, Verbose: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	defer rt.Shutdown("test end")

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Direct call as operator to session/whoami returns operator principal and full caps.
	var opWhoami struct {
		Principal string   `json:"principal"`
		Caps      []string `json:"capabilities"`
	}
	if err := client.Call(ctx, "session/whoami", nil, &opWhoami); err != nil {
		t.Fatalf("operator whoami: %v", err)
	}
	if opWhoami.Principal != "operator" {
		t.Errorf("operator principal = %q, want operator", opWhoami.Principal)
	}

	// 2. Operator calls app/com.gostalgia.echo/identity.
	// The app handler internally makes an IPC call to session/whoami.
	// It MUST see only Echo's scoped app identity and capabilities, never operator privileges.
	var appIdentity struct {
		Principal string   `json:"principal"`
		AppID     string   `json:"app_id"`
		Caps      []string `json:"capabilities"`
	}
	if err := client.Call(ctx, "app/com.gostalgia.echo/identity", nil, &appIdentity); err != nil {
		t.Fatalf("echo identity call failed: %v", err)
	}
	if appIdentity.Principal != "app" {
		t.Errorf("appIdentity.Principal = %q, want app", appIdentity.Principal)
	}
	if appIdentity.AppID != "com.gostalgia.echo" {
		t.Errorf("appIdentity.AppID = %q, want com.gostalgia.echo", appIdentity.AppID)
	}
	if len(appIdentity.Caps) != 1 || appIdentity.Caps[0] != "ipc" {
		t.Errorf("appIdentity.Caps = %v, want [ipc]", appIdentity.Caps)
	}
}

func TestAppPrivateStorageAndScopedGrantsE2E(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, Verbose: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	defer rt.Shutdown("test end")

	data, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if err != nil {
		t.Fatalf("read runtime.json: %v", err)
	}
	var rInfo struct {
		Endpoint string `json:"endpoint"`
		Token    string `json:"token"`
	}
	if err := json.Unmarshal(data, &rInfo); err != nil {
		t.Fatal(err)
	}

	appToken, ok := rt.Apps.AppToken("com.gostalgia.echo")
	if !ok || appToken == "" {
		t.Fatalf("expected app token for echo app")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Connect operator
	opConn, err := platform.DialIPC(rInfo.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	opClient, err := ipc.NewClient(opConn, rInfo.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer opClient.Close()

	// Seed shared document as operator
	if err := opClient.Call(ctx, "fs/save", map[string]any{
		"path":        "/users/guest/documents/shared.txt",
		"data_base64": "c2hhcmVkIGRvY3VtZW50", // "shared document"
	}, nil); err != nil {
		t.Fatalf("seed shared doc: %v", err)
	}

	// Connect app client
	appConn, err := platform.DialIPC(rInfo.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	appClient, err := ipc.NewClient(appConn, appToken)
	if err != nil {
		t.Fatal(err)
	}
	defer appClient.Close()

	// 1. App accesses its private storage (inherently permitted)
	testPayload := "cHJpdmF0ZSBhcHAgZGF0YQ==" // "private app data"
	if err := appClient.Call(ctx, "fs/write", map[string]any{
		"path":        "/apps/data/com.gostalgia.echo/test.txt",
		"data_base64": testPayload,
	}, nil); err != nil {
		t.Fatalf("app write to private storage failed: %v", err)
	}

	var readResp struct {
		DataBase64 string `json:"data_base64"`
	}
	if err := appClient.Call(ctx, "fs/read", map[string]any{
		"path": "/apps/data/com.gostalgia.echo/test.txt",
	}, &readResp); err != nil {
		t.Fatalf("app read from private storage failed: %v", err)
	}
	if readResp.DataBase64 != testPayload {
		t.Fatalf("read payload = %q, want %q", readResp.DataBase64, testPayload)
	}

	// 2. App denied access to other app private storage and ungranted paths
	if err := appClient.Call(ctx, "fs/read", map[string]any{
		"path": "/apps/data/com.other.app/secret.txt",
	}, nil); err == nil {
		t.Fatal("app read of other app private storage should have failed")
	}
	if err := appClient.Call(ctx, "fs/read", map[string]any{
		"path": "/users/guest/documents/shared.txt",
	}, nil); err == nil {
		t.Fatal("app read of shared doc before grant should have failed")
	}

	// 3. Operator issues grant to echo app
	var grantResp struct {
		Grant struct {
			ID string `json:"id"`
		} `json:"grant"`
	}
	if err := opClient.Call(ctx, "fs/grant", map[string]any{
		"app_id": "com.gostalgia.echo",
		"path":   "/users/guest/documents/shared.txt",
		"access": "read",
	}, &grantResp); err != nil {
		t.Fatalf("operator issue grant: %v", err)
	}
	grantID := grantResp.Grant.ID

	// 4. App reads shared document successfully
	if err := appClient.Call(ctx, "fs/read", map[string]any{
		"path": "/users/guest/documents/shared.txt",
	}, &readResp); err != nil {
		t.Fatalf("app read granted doc failed: %v", err)
	}

	// 5. App cannot write to read-only grant
	if err := appClient.Call(ctx, "fs/write", map[string]any{
		"path":        "/users/guest/documents/shared.txt",
		"data_base64": testPayload,
	}, nil); err == nil {
		t.Fatal("app write to read-only grant should have failed")
	}

	// 6. Operator revokes grant
	if err := opClient.Call(ctx, "fs/grant/revoke", map[string]string{
		"id": grantID,
	}, nil); err != nil {
		t.Fatalf("operator revoke grant: %v", err)
	}

	// 7. App immediate failure on revoked grant
	if err := appClient.Call(ctx, "fs/read", map[string]any{
		"path": "/users/guest/documents/shared.txt",
	}, nil); err == nil {
		t.Fatal("app read after grant revocation should have failed")
	}
}
