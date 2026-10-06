//go:build windows

package platform

import (
	"fmt"
	"os/exec"
)

// GetHostSecurityCapabilities reports sandbox capabilities on Windows.
func GetHostSecurityCapabilities() HostSecurityCapabilities {
	return HostSecurityCapabilities{
		Platform:  "windows",
		Supported: false,
		Reason:    "OS sandboxing is not supported on Windows host without container hypervisor",
	}
}

// ConfigureSandbox fails closed for sandbox requests on Windows, but allows trusted execution.
func ConfigureSandbox(cmd *exec.Cmd, policy ExecutionPolicy) (PostStartHook, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if policy.Isolation == "" || policy.Isolation == IsolationTrusted {
		return nil, nil
	}
	return nil, fmt.Errorf("%w: sandbox isolation policy %q is unsupported on Windows", ErrSandboxUnsupported, policy.Isolation)
}

// KillDescendants is a no-op on Windows.
func KillDescendants(pid int) {}
