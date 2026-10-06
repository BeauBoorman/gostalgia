//go:build !linux && !darwin && !windows

package platform

import (
	"fmt"
	"os/exec"
	"runtime"
)

// GetHostSecurityCapabilities reports sandbox capabilities on unsupported platforms.
func GetHostSecurityCapabilities() HostSecurityCapabilities {
	return HostSecurityCapabilities{
		Platform:  runtime.GOOS,
		Supported: false,
		Reason:    fmt.Sprintf("OS sandboxing is not implemented on %s", runtime.GOOS),
	}
}

// ConfigureSandbox fails closed for sandbox requests on unsupported platforms.
func ConfigureSandbox(cmd *exec.Cmd, policy ExecutionPolicy) (PostStartHook, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if policy.Isolation == "" || policy.Isolation == IsolationTrusted {
		return nil, nil
	}
	return nil, fmt.Errorf("%w: sandbox isolation policy %q is unsupported on %s", ErrSandboxUnsupported, policy.Isolation, runtime.GOOS)
}

// KillDescendants is a no-op on fallback platforms.
func KillDescendants(pid int) {}
