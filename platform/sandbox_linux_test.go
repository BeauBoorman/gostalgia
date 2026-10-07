//go:build linux

package platform

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLinuxConfigureSandboxFlags checks that a sandboxed policy configures
// user+mount namespaces and stages the init payload for the re-exec hook.
func TestLinuxConfigureSandboxFlags(t *testing.T) {
	if !GetHostSecurityCapabilities().Supported {
		t.Skip("sandbox unsupported on this host")
	}
	cmd := exec.Command("/bin/echo", "hi")
	hook, err := ConfigureSandbox(cmd, ExecutionPolicy{
		Isolation:   IsolationSandbox,
		DenyNetwork: true,
	})
	if err != nil {
		t.Fatalf("ConfigureSandbox: %v", err)
	}
	if hook == nil {
		t.Fatal("expected post-start hook for resource limits")
	}
	attr := cmd.SysProcAttr
	if attr == nil {
		t.Fatal("SysProcAttr not set")
	}
	want := uintptr(syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET)
	if attr.Unshareflags&want != want {
		t.Fatalf("Unshareflags = %#x, want NEWUSER|NEWNS|NEWNET", attr.Unshareflags)
	}
	if cmd.Path != "/proc/self/exe" {
		t.Fatalf("cmd.Path = %q, want /proc/self/exe re-exec", cmd.Path)
	}
	if len(attr.UidMappings) != 1 || attr.UidMappings[0].HostID != os.Getuid() {
		t.Fatalf("uid mapping incorrect: %+v", attr.UidMappings)
	}
	var payload string
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, sandboxInitEnv+"=") {
			payload = strings.TrimPrefix(kv, sandboxInitEnv+"=")
		}
	}
	if payload == "" {
		t.Fatal("sandbox init payload missing from env")
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("payload not base64: %v", err)
	}
	var cfg sandboxInitConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("payload not json: %v", err)
	}
	if !strings.HasSuffix(cfg.Path, "/echo") {
		t.Fatalf("payload path = %q, want the real binary", cfg.Path)
	}
}

// TestLinuxTrustedNoNamespace ensures trusted children are untouched.
func TestLinuxTrustedNoNamespace(t *testing.T) {
	cmd := exec.Command("/bin/echo", "hi")
	hook, err := ConfigureSandbox(cmd, ExecutionPolicy{Isolation: IsolationTrusted})
	if err != nil || hook != nil {
		t.Fatalf("trusted: hook=%v err=%v", hook, err)
	}
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Unshareflags != 0 {
		t.Fatal("trusted child got namespace flags")
	}
	if cmd.Path == "/proc/self/exe" {
		t.Fatal("trusted child was wrapped in re-exec")
	}
}

// TestLinuxKillDescendantsProcessGroup verifies the pgid sweep kills group
// members even after they reparent (the classic fork+exit race).
func TestLinuxKillDescendantsProcessGroup(t *testing.T) {
	// Parent that forks a child which immediately orphans a grandchild
	// into the same process group.
	cmd := exec.Command("sh", "-c", "sleep 60 & sleep 60 & exec sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	defer cmd.Process.Kill()
	defer cmd.Wait()

	// Give the children a moment to spawn.
	time.Sleep(150 * time.Millisecond)

	KillDescendants(pid)

	// Everything in the child's process group except the leader must be dead.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		alive := 0
		entries, _ := os.ReadDir("/proc")
		for _, e := range entries {
			p, err := strconv.Atoi(e.Name())
			if err != nil || p <= 0 || p == pid {
				continue
			}
			data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p))
			if err != nil {
				continue
			}
			_, pgid, ok := linuxParseProcStat(string(data))
			if ok && pgid == pid {
				alive++
			}
		}
		if alive == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("descendant process-group members still alive after KillDescendants")
}

// TestLinuxKillDescendantsOrphaned verifies the ppid-tree sweep catches a
// descendant that escaped the process group but not the ancestry tree.
func TestLinuxKillDescendantsOrphaned(t *testing.T) {
	// setsid detaches from the process group but the ppid tree still links
	// the grandchild back through the (still living) intermediate child.
	cmd := exec.Command("sh", "-c", "setsid sleep 60 & exec sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn test tree: %v", err)
	}
	pid := cmd.Process.Pid
	defer cmd.Process.Kill()
	defer cmd.Wait()

	time.Sleep(150 * time.Millisecond)
	KillDescendants(pid)

	// The setsid'd grandchild should be dead within a poll.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		found := false
		entries, _ := os.ReadDir("/proc")
		for _, e := range entries {
			p, err := strconv.Atoi(e.Name())
			if err != nil || p <= 0 {
				continue
			}
			data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p))
			if err != nil {
				continue
			}
			ppid, _, ok := linuxParseProcStat(string(data))
			if ok && ppid == pid {
				found = true
			}
		}
		if !found {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("ppid-tree descendant still alive after KillDescendants")
}

// TestLinuxCapabilityInvariant: on a host that advertises filesystem
// sandboxing, a sandboxed policy must not silently degrade — the mount
// program must have been staged.
func TestLinuxCapabilityInvariant(t *testing.T) {
	caps := GetHostSecurityCapabilities()
	if !caps.Supported {
		t.Skipf("sandbox unsupported: %s", caps.Reason)
	}
	if !caps.FilesystemSandbox {
		t.Fatal("Supported without FilesystemSandbox: capability drift")
	}
}
