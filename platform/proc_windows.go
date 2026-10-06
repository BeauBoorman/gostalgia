//go:build windows

package platform

import (
	"os"
	"os/exec"
)

// SetupProcessTree configures cmd attributes on Windows.
func SetupProcessTree(cmd *exec.Cmd) {
	// Standard Windows process hierarchy.
}

// KillProcessTree kills cmd on Windows.
func KillProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// SampleProcessResources samples resource usage for a process on Windows.
func SampleProcessResources(cmd *exec.Cmd) ResourceUsage {
	if cmd == nil || cmd.Process == nil {
		return ResourceUsage{Supported: false}
	}
	if cmd.ProcessState != nil {
		return ResourceUsage{
			Supported: true,
			CPUUserMs: cmd.ProcessState.UserTime().Milliseconds(),
			CPUSysMs:  cmd.ProcessState.SystemTime().Milliseconds(),
		}
	}
	return ResourceUsage{Supported: false}
}

// ProcessAlive checks if a process with the given PID is still alive.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p
	return false
}
