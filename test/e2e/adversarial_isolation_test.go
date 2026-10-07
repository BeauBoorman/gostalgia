package e2e

import (
	"context"
	"os"
	"path/filepath"
	goRuntime "runtime"
	"strings"
	"testing"
	"time"

	"gostalgia/internal/app"
	"gostalgia/internal/process"
	"gostalgia/internal/runtime"
	"gostalgia/platform"
	"gostalgia/sdk"
)

// TestSandboxEnforcementVsUnrestrictedLaunch verifies that advertised sandbox guarantees
// fail under an unrestricted launch ("trusted") and pass with policy enforcement enabled
// ("strict" or "sandbox").
func TestSandboxEnforcementVsUnrestrictedLaunch(t *testing.T) {
	caps := platform.GetHostSecurityCapabilities()
	if !caps.Supported {
		t.Skipf("host sandbox enforcement unsupported: %s", caps.Reason)
	}

	bin := getAdversarialBinary(t)
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("test end") })

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Scratch file in user home directory (outside temp and VFS) used to test host filesystem write protection.
	// On macOS Seatbelt, ReadOnlyFS restricts writes outside ephemeral runtime socket directories (/tmp, /var/folders).
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		homeDir = root
	}
	scratchFile := filepath.Join(homeDir, "gostalgia_adv_scratch_test.txt")
	defer os.Remove(scratchFile)

	// 1. Unrestricted App ("trusted")
	trustedMan := app.Manifest{
		ID:              "com.test.unrestricted",
		Name:            "Unrestricted App",
		Version:         "1.0.0",
		Mode:            sdk.ModeExternal,
		Executable:      bin,
		Isolation:       sdk.IsolationTrusted,
		Permissions:     []string{sdk.CapIPC},
		ProtocolVersion: 1,
	}
	must(t, rt.Apps.Registry().RegisterExternal(trustedMan))

	var trustedLaunch struct {
		PID int32 `json:"pid"`
	}
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": trustedMan.ID}, &trustedLaunch))
	defer func() {
		_ = client.Call(ctx, "app/stop", map[string]string{"id": trustedMan.ID}, nil)
	}()

	// Guarantee A: Descendant Process Creation
	// - Unrestricted launch CAN spawn a descendant process
	var spawnRes struct {
		Spawned bool   `json:"spawned"`
		Error   string `json:"error"`
	}
	must(t, client.Call(ctx, "app/com.test.unrestricted/spawn_descendant", nil, &spawnRes))
	if !spawnRes.Spawned {
		t.Fatalf("unrestricted launch unexpectedly failed to spawn descendant: %s", spawnRes.Error)
	}

	// Guarantee B: Host Filesystem Write
	// - Unrestricted launch CAN write to a host file outside VFS
	var writeRes struct {
		Written bool   `json:"written"`
		Error   string `json:"error"`
	}
	must(t, client.Call(ctx, "app/com.test.unrestricted/probe_write", map[string]string{"path": scratchFile}, &writeRes))
	if !writeRes.Written {
		t.Fatalf("unrestricted launch unexpectedly failed to write host scratch file: %s", writeRes.Error)
	}

	// Stop unrestricted app
	must(t, client.Call(ctx, "app/stop", map[string]string{"id": trustedMan.ID}, nil))

	// 2. Policy-Enforced App ("strict")
	strictMan := app.Manifest{
		ID:              "com.test.enforced",
		Name:            "Policy Enforced App",
		Version:         "1.0.0",
		Mode:            sdk.ModeExternal,
		Executable:      bin,
		Isolation:       sdk.IsolationStrict,
		Permissions:     []string{sdk.CapIPC},
		ProtocolVersion: 1,
	}
	must(t, rt.Apps.Registry().RegisterExternal(strictMan))

	var strictLaunch struct {
		PID int32 `json:"pid"`
	}
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": strictMan.ID}, &strictLaunch))
	defer func() {
		_ = client.Call(ctx, "app/stop", map[string]string{"id": strictMan.ID}, nil)
	}()

	// Guarantee A under Policy Enforcement:
	// - Strict launch CANNOT spawn a descendant process (blocked by Seatbelt or process monitor)
	var strictSpawnRes struct {
		Spawned bool   `json:"spawned"`
		Error   string `json:"error"`
	}
	if err := client.Call(ctx, "app/com.test.enforced/spawn_descendant", nil, &strictSpawnRes); err != nil {
		// Calling may return RPC error if process was immediately killed
		t.Logf("strict spawn RPC error (expected): %v", err)
	} else if strictSpawnRes.Spawned {
		t.Fatalf("strict policy-enforced launch unexpectedly spawned descendant process!")
	}

	// Guarantee B under Policy Enforcement:
	// - Strict launch CANNOT write to a host file (blocked by read-only Seatbelt or read-only mount)
	if caps.FilesystemSandbox && goRuntime.GOOS == "darwin" {
		var strictWriteRes struct {
			Written bool   `json:"written"`
			Error   string `json:"error"`
		}
		must(t, client.Call(ctx, "app/com.test.enforced/probe_write", map[string]string{"path": scratchFile}, &strictWriteRes))
		if strictWriteRes.Written {
			t.Fatalf("strict policy-enforced launch unexpectedly wrote to host scratch file!")
		}
		t.Logf("strict write prevented with error: %s", strictWriteRes.Error)
	}

	// Guarantee C under Policy Enforcement:
	// - Strict launch CANNOT reach external network
	var strictNetRes struct {
		Connected bool   `json:"connected"`
		Error     string `json:"error"`
	}
	must(t, client.Call(ctx, "app/com.test.enforced/probe_network", map[string]string{"target": "1.1.1.1:80"}, &strictNetRes))
	if strictNetRes.Connected {
		t.Fatalf("strict policy-enforced launch unexpectedly connected to external network!")
	}
	t.Logf("strict network prevented with error: %s", strictNetRes.Error)
}

// TestAdversarialHostFilesystemAccess exercises unauthorized host filesystem read/write
// and VFS escape attempts.
func TestAdversarialHostFilesystemAccess(t *testing.T) {
	caps := platform.GetHostSecurityCapabilities()
	if !caps.Supported {
		t.Skipf("host sandbox unsupported: %s", caps.Reason)
	}

	bin := getAdversarialBinary(t)
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("test end") })

	man := app.Manifest{
		ID:              "com.test.fsaccess",
		Name:            "FS Access App",
		Version:         "1.0.0",
		Mode:            sdk.ModeExternal,
		Executable:      bin,
		Isolation:       sdk.IsolationStrict,
		Permissions:     []string{sdk.CapIPC},
		ProtocolVersion: 1,
	}
	must(t, rt.Apps.Registry().RegisterExternal(man))

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var launch struct {
		PID int32 `json:"pid"`
	}
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": man.ID}, &launch))
	defer func() {
		_ = client.Call(ctx, "app/stop", map[string]string{"id": man.ID}, nil)
	}()

	// 1. Probing write to protected host path (/etc/forbidden.txt) must fail
	var writeRes struct {
		Written bool   `json:"written"`
		Error   string `json:"error"`
	}
	must(t, client.Call(ctx, "app/com.test.fsaccess/probe_write", map[string]string{"path": "/etc/forbidden.txt"}, &writeRes))
	if writeRes.Written {
		t.Fatal("sandboxed app unexpectedly wrote to /etc/forbidden.txt")
	}

	// 2. Probing read of runtime.json: the app is NOT passed the runtime.json
	// path, and even if it guesses <root>/runtime.json the environment root is
	// masked from sandboxed children (#84): reads must fail outright on hosts
	// that advertise filesystem sandboxing.
	runtimeJSON := filepath.Join(root, "runtime.json")
	var readRes struct {
		Read    bool   `json:"read"`
		Content string `json:"content"`
		Error   string `json:"error"`
	}
	must(t, client.Call(ctx, "app/com.test.fsaccess/probe_read", map[string]string{"path": runtimeJSON}, &readRes))
	if caps.FilesystemSandbox {
		if readRes.Read {
			t.Fatalf("strict app read masked runtime.json: %q", readRes.Content)
		}
		t.Logf("runtime.json read denied: %s", readRes.Error)
	} else if readRes.Read && strings.Contains(readRes.Content, "token") {
		t.Logf("runtime.json read note: read=%v, err=%s", readRes.Read, readRes.Error)
	}

	// 3. Probing VFS escape via dot-segments must fail closed
	var escapeRes struct {
		Escaped bool   `json:"escaped"`
		Error   string `json:"error"`
	}
	must(t, client.Call(ctx, "app/com.test.fsaccess/probe_vfs_escape", map[string]string{"path": "/users/guest/../../../etc/passwd"}, &escapeRes))
	if escapeRes.Escaped {
		t.Fatal("VFS escape attempt succeeded!")
	}
	if !strings.Contains(escapeRes.Error, "dot segments") && !strings.Contains(escapeRes.Error, "permission denied") && !strings.Contains(escapeRes.Error, "invalid path") {
		t.Errorf("unexpected VFS escape error: %s", escapeRes.Error)
	}

	// 4. Probing VFS escape to other app's private partition must fail closed
	must(t, client.Call(ctx, "app/com.test.fsaccess/probe_vfs_escape", map[string]string{"path": "/apps/data/com.other.app/secret.txt"}, &escapeRes))
	if escapeRes.Escaped {
		t.Fatal("VFS cross-app private partition read succeeded!")
	}
}

// TestAdversarialCredentialLeakage tests that operator tokens and sensitive parent
// environment variables never leak to child processes.
func TestAdversarialCredentialLeakage(t *testing.T) {
	bin := getAdversarialBinary(t)
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("test end") })

	man := app.Manifest{
		ID:              "com.test.credleak",
		Name:            "Cred Leak App",
		Version:         "1.0.0",
		Mode:            sdk.ModeExternal,
		Executable:      bin,
		Isolation:       sdk.IsolationTrusted,
		Permissions:     []string{sdk.CapIPC},
		ProtocolVersion: 1,
	}
	must(t, rt.Apps.Registry().RegisterExternal(man))

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var launch struct {
		PID int32 `json:"pid"`
	}
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": man.ID}, &launch))
	defer func() {
		_ = client.Call(ctx, "app/stop", map[string]string{"id": man.ID}, nil)
	}()

	// Verify environment of app
	var credRes struct {
		LeakedOperatorToken bool     `json:"leaked_operator_token"`
		HasAppToken         bool     `json:"has_app_token"`
		Environ             []string `json:"environ"`
	}
	must(t, client.Call(ctx, "app/com.test.credleak/probe_credentials", map[string]string{
		"runtime_json_path": filepath.Join(root, "runtime.json"),
	}, &credRes))

	if credRes.LeakedOperatorToken {
		t.Fatal("operator token was leaked in child environment variables!")
	}
	if !credRes.HasAppToken {
		t.Fatal("child was not provided GOSTALGIA_APP_TOKEN")
	}

	// Verify child process spawned by app also has sanitized environment
	var childEnvRes struct {
		Success bool     `json:"success"`
		Environ []string `json:"environ"`
		Error   string   `json:"error"`
	}
	must(t, client.Call(ctx, "app/com.test.credleak/probe_child_env", nil, &childEnvRes))
	if !childEnvRes.Success {
		t.Fatalf("probe_child_env failed: %s", childEnvRes.Error)
	}
	for _, env := range childEnvRes.Environ {
		upper := strings.ToUpper(env)
		if strings.Contains(upper, "OPERATOR_TOKEN") {
			t.Fatalf("child process inherited operator token: %s", env)
		}
	}
}

// TestAdversarialPermissionBorrowing tests that an external application cannot
// invoke privileged runtime methods or borrow administrative capabilities.
func TestAdversarialPermissionBorrowing(t *testing.T) {
	bin := getAdversarialBinary(t)
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("test end") })

	man := app.Manifest{
		ID:              "com.test.deputy",
		Name:            "Confused Deputy App",
		Version:         "1.0.0",
		Mode:            sdk.ModeExternal,
		Executable:      bin,
		Isolation:       sdk.IsolationTrusted,
		Permissions:     []string{sdk.CapIPC},
		ProtocolVersion: 1,
	}
	must(t, rt.Apps.Registry().RegisterExternal(man))

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var launch struct {
		PID int32 `json:"pid"`
	}
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": man.ID}, &launch))
	defer func() {
		_ = client.Call(ctx, "app/stop", map[string]string{"id": man.ID}, nil)
	}()

	var borrowRes struct {
		ShutdownAllowed  bool              `json:"shutdown_allowed"`
		ProcStopAllowed  bool              `json:"proc_stop_allowed"`
		FsGrantAllowed   bool              `json:"fs_grant_allowed"`
		AppLaunchAllowed bool              `json:"app_launch_allowed"`
		Whoami           map[string]any    `json:"whoami"`
		Errors           map[string]string `json:"errors"`
	}
	must(t, client.Call(ctx, "app/com.test.deputy/borrow_permissions", nil, &borrowRes))

	if borrowRes.ShutdownAllowed {
		t.Fatal("app was unexpectedly allowed to invoke sys/shutdown!")
	}
	if borrowRes.ProcStopAllowed {
		t.Fatal("app was unexpectedly allowed to invoke proc/stop!")
	}
	if borrowRes.FsGrantAllowed {
		t.Fatal("app was unexpectedly allowed to invoke fs/grant!")
	}
	if borrowRes.AppLaunchAllowed {
		t.Fatal("app was unexpectedly allowed to invoke app/launch!")
	}

	// Verify whoami reports app principal
	if principal, ok := borrowRes.Whoami["principal"].(string); !ok || principal != "app" {
		t.Errorf("expected principal = 'app', got %v", borrowRes.Whoami["principal"])
	}
	if appID, ok := borrowRes.Whoami["app_id"].(string); !ok || appID != man.ID {
		t.Errorf("expected app_id = %q, got %v", man.ID, borrowRes.Whoami["app_id"])
	}
}

// TestAdversarialCPUAndMemoryAbuse tests that heavy CPU/memory consumption can run
// without wedging the runtime's supervision or operator IPC communication.
func TestAdversarialCPUAndMemoryAbuse(t *testing.T) {
	bin := getAdversarialBinary(t)
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("test end") })

	man := app.Manifest{
		ID:              "com.test.resource",
		Name:            "Resource Abuse App",
		Version:         "1.0.0",
		Mode:            sdk.ModeExternal,
		Executable:      bin,
		Isolation:       sdk.IsolationTrusted,
		Permissions:     []string{sdk.CapIPC},
		ProtocolVersion: 1,
	}
	must(t, rt.Apps.Registry().RegisterExternal(man))

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var launch struct {
		PID int32 `json:"pid"`
	}
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": man.ID}, &launch))
	defer func() {
		_ = client.Call(ctx, "app/stop", map[string]string{"id": man.ID}, nil)
	}()

	// 1. CPU Burn: run 4 concurrent spinning loops for 150ms
	var cpuRes struct {
		Completed  bool   `json:"completed"`
		Operations uint64 `json:"operations"`
	}
	must(t, client.Call(ctx, "app/com.test.resource/burn_cpu", map[string]int{
		"duration_ms": 150,
		"workers":     4,
	}, &cpuRes))
	if !cpuRes.Completed {
		t.Fatal("burn_cpu did not complete")
	}

	// Ensure runtime remains fully responsive to operator calls while app works
	var sysStatus map[string]any
	must(t, client.Call(ctx, "sys/status", nil, &sysStatus))
	if sysStatus == nil {
		t.Fatal("sys/status returned nil")
	}

	// 2. Memory Consumption: allocate and touch 32 MB
	var memRes struct {
		AllocatedMB int    `json:"allocated_mb"`
		Error       string `json:"error"`
	}
	must(t, client.Call(ctx, "app/com.test.resource/consume_memory", map[string]int{
		"megabytes": 32,
	}, &memRes))
	if memRes.AllocatedMB != 32 {
		t.Fatalf("consume_memory allocated %d MB, want 32", memRes.AllocatedMB)
	}

	// Verify proc/info reports process state
	var procInfo process.Info
	must(t, client.Call(ctx, "proc/info", map[string]any{"id": launch.PID}, &procInfo))
	if procInfo.State != process.StateRunning {
		t.Fatalf("proc state = %s, want running", procInfo.State)
	}
}

// TestAdversarialUncooperativeShutdown tests that an application refusing to stop
// is forcibly terminated by the supervisor within the deadline, without leaking
// state or hung processes.
func TestAdversarialUncooperativeShutdown(t *testing.T) {
	bin := getAdversarialBinary(t)
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root})
	must(t, err)
	t.Cleanup(func() { rt.Shutdown("test end") })

	man := app.Manifest{
		ID:              "com.test.uncooperative",
		Name:            "Uncooperative App",
		Version:         "1.0.0",
		Mode:            sdk.ModeExternal,
		Executable:      bin,
		Isolation:       sdk.IsolationTrusted,
		Permissions:     []string{sdk.CapIPC},
		ProtocolVersion: 1,
	}
	must(t, rt.Apps.Registry().RegisterExternal(man))

	client := dialRunning(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var launch struct {
		PID int32 `json:"pid"`
	}
	must(t, client.Call(ctx, "app/launch", map[string]string{"id": man.ID}, &launch))

	// Configure app to hang on stop (sleep for 30s during Stop)
	var hangRes struct {
		HangConfigured bool `json:"hang_configured"`
	}
	must(t, client.Call(ctx, "app/com.test.uncooperative/hang_on_stop", map[string]any{
		"hang":        true,
		"duration_ms": 30000,
	}, &hangRes))
	if !hangRes.HangConfigured {
		t.Fatal("failed to configure hang on stop")
	}

	// Stop with a short timeout: 2 seconds
	// The process supervisor must escalate to KillProcessTree / SIGKILL and force termination.
	stopStart := time.Now()
	stopErr := client.Call(ctx, "app/stop", map[string]string{"id": man.ID}, nil)
	stopDuration := time.Since(stopStart)

	t.Logf("app/stop result: err=%v, duration=%s", stopErr, stopDuration)
	if stopDuration > 10*time.Second {
		t.Fatalf("app/stop took %s, supervisor failed to terminate uncooperative app promptly", stopDuration)
	}

	// Verify app is no longer running
	var appsList []map[string]any
	must(t, client.Call(ctx, "app/list", nil, &appsList))
	for _, a := range appsList {
		if a["id"] == man.ID && a["running"] == true {
			t.Fatalf("uncooperative app is still listed as running!")
		}
	}
}
