package e2e

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCoreDependencyBoundary(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}",
		"gostalgia/internal/runtime", "gostalgia/sdk", "gostalgia/apps/echo", "gostalgia/cmd/gctl")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect dependency graph: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if !strings.HasPrefix(pkg, "gostalgia/") {
			t.Errorf("core/SDK dependency escaped stdlib boundary: %s", pkg)
		}
	}
}
