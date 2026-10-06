package shell

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gostalgia/internal/pkg"
)

type packageCaller struct {
	method string
	params pkg.Params
}

func (c *packageCaller) Call(_ context.Context, method string, params, result any) error {
	c.method = method
	c.params = params.(pkg.Params)
	*result.(*json.RawMessage) = json.RawMessage(`{"installed":true}`)
	return nil
}

func TestPackageShellCommands(t *testing.T) {
	for _, line := range []string{`pkg install "downloads/app.gpkg" --confirm-permissions`, `package install C:\users\guest\downloads\app.gpkg --confirm-permissions`} {
		c := &packageCaller{}
		text, cwd, quit, err := command(context.Background(), c, "/users/guest", line)
		if err != nil || quit || cwd != "/users/guest" || !strings.Contains(text, "installed") {
			t.Fatal(text, err)
		}
		if c.method != "pkg/install" || c.params.Path != "/users/guest/downloads/app.gpkg" || !c.params.ConfirmPermissions {
			t.Fatalf("bad package call: %s %+v", c.method, c.params)
		}
	}
	c := &packageCaller{}
	if _, _, _, err := command(context.Background(), c, "/", "pkg rollback"); err == nil || c.method != "" {
		t.Fatal("invalid command reached IPC")
	}
}
