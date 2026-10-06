package platform

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestPolicyForIsolation(t *testing.T) {
	trusted, err := PolicyForIsolation(IsolationTrusted)
	if err != nil || trusted.Isolation != IsolationTrusted || trusted.DenyNetwork {
		t.Fatalf("unexpected trusted policy: %+v, err: %v", trusted, err)
	}

	sandbox, err := PolicyForIsolation(IsolationSandbox)
	if err != nil || sandbox.Isolation != IsolationSandbox || !sandbox.DenyNetwork {
		t.Fatalf("unexpected sandbox policy: %+v, err: %v", sandbox, err)
	}

	strict, err := PolicyForIsolation(IsolationStrict)
	if err != nil || strict.Isolation != IsolationStrict || !strict.DenyNetwork || !strict.DenyDescendants || !strict.ReadOnlyFS {
		t.Fatalf("unexpected strict policy: %+v, err: %v", strict, err)
	}

	_, err = PolicyForIsolation("bogus")
	if err == nil {
		t.Fatal("expected error for bogus isolation level")
	}
}

func TestGetHostSecurityCapabilities(t *testing.T) {
	caps := GetHostSecurityCapabilities()
	if caps.Platform == "" {
		t.Fatal("expected non-empty platform")
	}

	switch runtime.GOOS {
	case "linux":
		if caps.Platform != "linux" {
			t.Fatalf("expected platform linux, got %s", caps.Platform)
		}
		if caps.Supported && !caps.NetworkIsolation {
			t.Fatal("expected network isolation when supported on linux")
		}
	case "windows":
		if caps.Supported {
			t.Fatal("expected supported=false on windows")
		}
		if caps.Reason == "" {
			t.Fatal("expected honest reason on windows")
		}
	case "darwin":
		if caps.Platform != "darwin" {
			t.Fatalf("expected platform darwin, got %s", caps.Platform)
		}
	}
}

func TestConfigureSandboxTrusted(t *testing.T) {
	cmd := exec.Command("echo", "hello")
	hook, err := ConfigureSandbox(cmd, ExecutionPolicy{Isolation: IsolationTrusted})
	if err != nil {
		t.Fatalf("trusted policy should never fail: %v", err)
	}
	if hook != nil {
		t.Fatal("expected nil hook for trusted policy")
	}
}
