//go:build darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

var (
	darwinCapsOnce sync.Once
	darwinCaps     HostSecurityCapabilities
)

// GetHostSecurityCapabilities probes and returns the sandbox enforcement mechanisms
// available on this Darwin/macOS host.
func GetHostSecurityCapabilities() HostSecurityCapabilities {
	darwinCapsOnce.Do(func() {
		caps := HostSecurityCapabilities{
			Platform: "darwin",
		}
		if _, err := os.Stat("/usr/bin/sandbox-exec"); err == nil {
			caps.Supported = true
			caps.NetworkIsolation = true
			caps.DescendantControl = true
			caps.FilesystemSandbox = true
			caps.ResourceLimits = true
		} else {
			caps.Supported = false
			caps.Reason = "/usr/bin/sandbox-exec not found"
		}
		darwinCaps = caps
	})
	return darwinCaps
}

// ConfigureSandbox sets up host-level confinement for cmd on macOS using Seatbelt.
func ConfigureSandbox(cmd *exec.Cmd, policy ExecutionPolicy) (PostStartHook, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if policy.Isolation == "" || policy.Isolation == IsolationTrusted {
		return nil, nil
	}

	caps := GetHostSecurityCapabilities()
	if !caps.Supported {
		return nil, fmt.Errorf("%w: %s", ErrSandboxUnsupported, caps.Reason)
	}

	profile := buildDarwinSandboxProfile(policy)
	origPath := cmd.Path
	origArgs := append([]string(nil), cmd.Args...)
	if len(origArgs) > 0 {
		origArgs = origArgs[1:]
	}

	cmd.Path = "/usr/bin/sandbox-exec"
	sandboxArgs := []string{"sandbox-exec", "-p", profile, origPath}
	cmd.Args = append(sandboxArgs, origArgs...)

	return nil, nil
}

func buildDarwinSandboxProfile(policy ExecutionPolicy) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	b.WriteString("(allow process-exec)\n")
	b.WriteString("(allow ipc-posix*)\n")
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow mach-lookup)\n")
	b.WriteString("(allow signal (target self))\n")

	if policy.DenyDescendants {
		b.WriteString("(deny process-fork)\n")
	} else {
		b.WriteString("(allow process-fork)\n")
	}

	if policy.DenyNetwork {
		b.WriteString("(deny network*)\n")
	} else {
		b.WriteString("(allow network*)\n")
	}

	b.WriteString("(allow file-read*)\n")
	if policy.ReadOnlyFS {
		b.WriteString("(deny file-write*)\n")
		for _, p := range policy.AllowedPaths {
			if p != "" {
				b.WriteString(fmt.Sprintf("(allow file-write* (subpath %q))\n", p))
			}
		}
	} else {
		b.WriteString("(allow file-write*)\n")
	}

	return b.String()
}

// KillDescendants is a no-op on macOS as process-fork denial is enforced by Seatbelt.
func KillDescendants(pid int) {}
