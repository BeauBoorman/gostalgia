//go:build linux

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

const (
	linuxRLimitCPU    = 0
	linuxRLimitNProc  = 6
	linuxRLimitNoFile = 7
	linuxRLimitAS     = 9
)

var (
	linuxCapsOnce sync.Once
	linuxCaps     HostSecurityCapabilities
)

// GetHostSecurityCapabilities probes and returns the sandbox enforcement mechanisms
// available on this Linux host.
func GetHostSecurityCapabilities() HostSecurityCapabilities {
	linuxCapsOnce.Do(func() {
		caps := HostSecurityCapabilities{
			Platform:          "linux",
			Supported:         true,
			ResourceLimits:    true,
			DescendantControl: true,
		}

		// Check if user and net namespaces are enabled in the kernel
		if _, err := os.Stat("/proc/self/ns/user"); err != nil {
			caps.Supported = false
			caps.Reason = "user namespace unsupported by kernel"
		} else if _, err := os.Stat("/proc/self/ns/net"); err != nil {
			caps.Supported = false
			caps.Reason = "network namespace unsupported by kernel"
		} else if data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil && strings.TrimSpace(string(data)) == "0" {
			caps.Supported = false
			caps.Reason = "unprivileged user namespaces disabled by sysctl"
		} else if data, err := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); err == nil && strings.TrimSpace(string(data)) == "1" {
			caps.Supported = false
			caps.Reason = "unprivileged user namespaces restricted by AppArmor"
		}

		// Active probe: verify that unprivileged user namespace clone is permitted on this host.
		if caps.Supported {
			sh, err := exec.LookPath("sh")
			if err != nil {
				sh = "/bin/sh"
			}
			cmd := exec.Command(sh, "-c", "exit 0")
			cmd.SysProcAttr = &syscall.SysProcAttr{
				Cloneflags: syscall.CLONE_NEWUSER,
			}
			if err := cmd.Run(); err != nil {
				caps.Supported = false
				caps.Reason = fmt.Sprintf("unprivileged user namespace restricted by host: %v", err)
			}
		}

		if caps.Supported {
			caps.NetworkIsolation = true
			caps.FilesystemSandbox = true
		}

		linuxCaps = caps
	})
	return linuxCaps
}

func setPrlimit(pid int, resource int, limit uint64) error {
	if limit == 0 {
		return nil
	}
	var rlim syscall.Rlimit
	rlim.Cur = limit
	rlim.Max = limit
	_, _, errno := syscall.RawSyscall6(
		syscall.SYS_PRLIMIT64,
		uintptr(pid),
		uintptr(resource),
		uintptr(unsafe.Pointer(&rlim)),
		0, 0, 0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

// ConfigureSandbox sets up host-level confinement for cmd on Linux.
func ConfigureSandbox(cmd *exec.Cmd, policy ExecutionPolicy) (PostStartHook, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if policy.Isolation == "" || policy.Isolation == IsolationTrusted {
		return nil, nil
	}

	caps := GetHostSecurityCapabilities()
	if !caps.Supported && (policy.Isolation == IsolationSandbox || policy.Isolation == IsolationStrict || policy.DenyNetwork) {
		return nil, fmt.Errorf("%w: %s", ErrSandboxUnsupported, caps.Reason)
	}

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL

	if policy.DenyNetwork {
		cmd.SysProcAttr.Unshareflags |= syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		}
		cmd.SysProcAttr.GidMappingsEnableSetgroups = false
	}

	postHook := func(pid int) error {
		if pid <= 0 {
			return nil
		}
		if policy.MaxMemoryBytes > 0 {
			if err := setPrlimit(pid, linuxRLimitAS, policy.MaxMemoryBytes); err != nil {
				return fmt.Errorf("platform: set RLIMIT_AS: %w", err)
			}
		}
		if policy.MaxOpenFiles > 0 {
			if err := setPrlimit(pid, linuxRLimitNoFile, policy.MaxOpenFiles); err != nil {
				return fmt.Errorf("platform: set RLIMIT_NOFILE: %w", err)
			}
		}
		if policy.MaxCPUSeconds > 0 {
			if err := setPrlimit(pid, linuxRLimitCPU, policy.MaxCPUSeconds); err != nil {
				return fmt.Errorf("platform: set RLIMIT_CPU: %w", err)
			}
		}
		if policy.MaxProcesses > 0 {
			if err := setPrlimit(pid, linuxRLimitNProc, policy.MaxProcesses); err != nil {
				return fmt.Errorf("platform: set RLIMIT_NPROC: %w", err)
			}
		}
		return nil
	}

	return postHook, nil
}

// KillDescendants scans all threads in /proc/<pid>/task/*/children and immediately
// sends SIGKILL to any spawned descendant processes to enforce DenyDescendants.
func KillDescendants(pid int) {
	if pid <= 0 {
		return
	}
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return
	}
	for _, task := range tasks {
		tid := task.Name()
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/children", pid, tid))
		if err != nil || len(data) == 0 {
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			if cpid, err := strconv.Atoi(field); err == nil && cpid > 0 {
				_ = syscall.Kill(cpid, syscall.SIGKILL)
			}
		}
	}
}
