package platform

import (
	"os/exec"
	"testing"
)

func TestSampleProcessResourcesNil(t *testing.T) {
	u := SampleProcessResources(nil)
	if u.Supported {
		t.Errorf("expected Supported=false for nil cmd, got true")
	}

	cmd := &exec.Cmd{}
	u = SampleProcessResources(cmd)
	if u.Supported {
		t.Errorf("expected Supported=false for cmd without running process, got true")
	}
}

func TestKillProcessTreeNil(t *testing.T) {
	if err := KillProcessTree(nil); err != nil {
		t.Errorf("KillProcessTree(nil) error: %v", err)
	}
	if err := KillProcessTree(&exec.Cmd{}); err != nil {
		t.Errorf("KillProcessTree(&exec.Cmd{}) error: %v", err)
	}
}
