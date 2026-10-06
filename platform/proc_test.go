package platform

import (
	"os/exec"
	"testing"
)

func TestSampleProcessResources(t *testing.T) {
	u := SampleProcessResources(-1)
	if u.Supported {
		t.Errorf("expected Supported=false for negative pid, got true")
	}

	u = SampleProcessResources(0)
	if u.Supported {
		t.Errorf("expected Supported=false for zero pid, got true")
	}

	stateUsage := SampleProcessState(nil)
	if stateUsage.Supported {
		t.Errorf("expected Supported=false for nil ProcessState, got true")
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
