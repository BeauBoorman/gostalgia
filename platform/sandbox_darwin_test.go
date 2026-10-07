//go:build darwin

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDarwinProfileDeniesNetwork checks that a DenyNetwork profile contains no
// unix-socket allowances. Child IPC rides an inherited pre-connected
// descriptor, so no connect/bind capability is ever granted to a confined
// process (remote unix-socket cannot be path-scoped by Seatbelt).
func TestDarwinProfileDeniesNetwork(t *testing.T) {
	p := buildDarwinSandboxProfile(ExecutionPolicy{
		Isolation:   IsolationSandbox,
		DenyNetwork: true,
	}, "/bin/echo")
	if !strings.Contains(p, "(deny network*)") {
		t.Fatalf("profile missing deny network*:\n%s", p)
	}
	for _, bad := range []string{"remote unix-socket", "local unix-socket", "network-outbound", "network-inbound", "network-bind"} {
		if strings.Contains(p, bad) {
			t.Fatalf("DenyNetwork profile still grants %s:\n%s", bad, p)
		}
	}
}

// TestDarwinProfileReadWhitelist verifies the #84 read boundary: sandboxed
// children may read execution essentials only; there is no bare
// (allow file-read*), and masked paths are carved out via require-not.
func TestDarwinProfileReadWhitelist(t *testing.T) {
	p := buildDarwinSandboxProfile(ExecutionPolicy{
		Isolation:   IsolationSandbox,
		DenyNetwork: true,
		MaskedPaths: []string{"/var/folders/x/T/envroot"},
	}, "/tmp/appbin")

	if strings.Contains(p, "(allow file-read*)\n") {
		t.Fatalf("profile still grants unconditional file-read*:\n%s", p)
	}
	if !strings.Contains(p, `(literal "/")`) {
		t.Fatalf("profile missing root literal needed for path resolution:\n%s", p)
	}
	if !strings.Contains(p, `(subpath "/usr")`) || !strings.Contains(p, `(subpath "/System")`) {
		t.Fatalf("profile missing system read roots:\n%s", p)
	}
	// Executable is allowlisted as a literal in raw and canonical forms.
	if !strings.Contains(p, `(literal "/tmp/appbin")`) {
		t.Fatalf("profile missing executable literal:\n%s", p)
	}
	// The masked path is carved out of every covering whitelist prefix for
	// both its raw and canonical spelling.
	if !strings.Contains(p, `(require-not (subpath "/var/folders/x/T/envroot"))`) ||
		!strings.Contains(p, `(require-not (subpath "/private/var/folders/x/T/envroot"))`) {
		t.Fatalf("profile missing mask carve for env root:\n%s", p)
	}
	// Path resolution needs metadata on ancestor dirs (/var, /private/var).
	if !strings.Contains(p, "(allow file-read-metadata") ||
		!strings.Contains(p, `(literal "/var")`) {
		t.Fatalf("profile missing ancestor metadata literals:\n%s", p)
	}
}

// TestDarwinProfileStrictFilesystem checks the strict tier: writes are denied
// except scratch dirs and operator-granted AllowedPaths.
func TestDarwinProfileStrictFilesystem(t *testing.T) {
	allowed := filepath.Join(t.TempDir(), "workdir")
	p := buildDarwinSandboxProfile(ExecutionPolicy{
		Isolation:    IsolationStrict,
		DenyNetwork:  true,
		ReadOnlyFS:   true,
		AllowedPaths: []string{allowed},
	}, "/tmp/appbin")

	if !strings.Contains(p, "(deny file-write*)") {
		t.Fatalf("strict profile missing deny file-write*:\n%s", p)
	}
	writeIdx := strings.Index(p, "(allow file-write*")
	if writeIdx < 0 {
		t.Fatalf("strict profile missing writable exceptions:\n%s", p)
	}
	if !strings.Contains(p[writeIdx:], sbPathFilter(allowed, nil)) &&
		!strings.Contains(p[writeIdx:], `(subpath "`+allowed+`")`) {
		t.Fatalf("strict profile missing AllowedPaths write grant %q:\n%s", allowed, p[writeIdx:])
	}
}

// TestDarwinProfileDenyDescendants maps DenyDescendants to Seatbelt's
// process-fork deny — on darwin this is prevention, not polling.
func TestDarwinProfileDenyDescendants(t *testing.T) {
	p := buildDarwinSandboxProfile(ExecutionPolicy{
		Isolation:       IsolationStrict,
		DenyNetwork:     true,
		DenyDescendants: true,
	}, "/tmp/appbin")
	if !strings.Contains(p, "(deny process-fork)") {
		t.Fatalf("strict profile missing deny process-fork:\n%s", p)
	}
	p2 := buildDarwinSandboxProfile(ExecutionPolicy{
		Isolation:   IsolationSandbox,
		DenyNetwork: true,
	}, "/tmp/appbin")
	if strings.Contains(p2, "(deny process-fork)") {
		t.Fatalf("sandbox tier should not deny process-fork:\n%s", p2)
	}
}

// TestDarwinProfileAppliesAndMasks is the end-to-end Seatbelt check: run a
// shell inside the generated profile and verify the masked path is unreadable
// while the system is still usable.
func TestDarwinProfileAppliesAndMasks(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec unavailable")
	}
	masked := t.TempDir()
	secret := filepath.Join(masked, "secret.txt")
	if err := os.WriteFile(secret, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := buildDarwinSandboxProfile(ExecutionPolicy{
		Isolation:   IsolationSandbox,
		DenyNetwork: true,
		MaskedPaths: []string{masked},
	}, "/bin/sh")

	// A whitelisted read must work...
	out, err := exec.Command("sandbox-exec", "-p", p, "/bin/sh", "-c", "cat /etc/hosts >/dev/null && echo ok").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("whitelisted read failed: %v (%s)", err, out)
	}
	// ...and the masked dir must be unreadable.
	out, err = exec.Command("sandbox-exec", "-p", p, "/bin/sh", "-c",
		"cat "+secret+" 2>/dev/null && echo LEAK || echo denied").CombinedOutput()
	if err != nil && !strings.Contains(string(out), "denied") {
		t.Fatalf("masked read probe failed unexpectedly: %v (%s)", err, out)
	}
	if strings.Contains(string(out), "LEAK") {
		t.Fatalf("masked path %s was readable under sandbox profile", masked)
	}
}

// TestDarwinTrustedNoProfile: trusted children get no confinement at all.
func TestDarwinTrustedNoProfile(t *testing.T) {
	cmd := exec.Command("echo", "hi")
	origPath, origArgs := cmd.Path, append([]string(nil), cmd.Args...)
	hook, err := ConfigureSandbox(cmd, ExecutionPolicy{Isolation: IsolationTrusted})
	if err != nil || hook != nil {
		t.Fatalf("trusted configure: hook=%v err=%v", hook, err)
	}
	if cmd.Path != origPath || len(cmd.Args) != len(origArgs) {
		t.Fatal("trusted child command was modified")
	}
}
