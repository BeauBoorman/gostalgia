//go:build unix

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// SetupProcessTree configures cmd so that it and any child processes it spawns
// run in their own process group, enabling clean process-tree termination.
func SetupProcessTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// KillProcessTree kills cmd and all its descendant processes by sending
// SIGKILL to the process group.
func KillProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if pid > 0 {
		// Send SIGKILL to the entire process group (-pid).
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	return cmd.Process.Kill()
}

// SampleProcessResources samples resource usage for an active process by PID.
// Unsupported metrics or platforms return Supported: false.
func SampleProcessResources(pid int) ResourceUsage {
	if pid <= 0 {
		return ResourceUsage{Supported: false}
	}

	// Try reading Linux /proc/<pid>/statm and /proc/<pid>/stat
	usage, ok := readLinuxProc(pid)
	if ok {
		return usage
	}

	// Non-Linux Unix or unsupported procfs: report unavailable
	return ResourceUsage{Supported: false}
}

func readLinuxProc(pid int) (ResourceUsage, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return ResourceUsage{}, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return ResourceUsage{}, false
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return ResourceUsage{}, false
	}
	pageSize := uint64(os.Getpagesize())
	rssBytes := pages * pageSize

	var userMs, sysMs int64
	if statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		statFields := strings.Fields(string(statData))
		if len(statFields) >= 15 {
			if u, err := strconv.ParseInt(statFields[13], 10, 64); err == nil {
				userMs = u * 10
			}
			if s, err := strconv.ParseInt(statFields[14], 10, 64); err == nil {
				sysMs = s * 10
			}
		}
	}

	return ResourceUsage{
		Supported:   true,
		MemoryBytes: rssBytes,
		CPUUserMs:   userMs,
		CPUSysMs:    sysMs,
	}, true
}

// ProcessAlive checks if a process with the given PID is still alive.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
