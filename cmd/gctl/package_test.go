package main

import (
	"context"
	"io"
	"testing"

	"gostalgia/internal/runtime"
)

func TestPackageCLIOverIPC(t *testing.T) {
	ctx := context.Background()
	rt, err := runtime.Boot(ctx, runtime.Options{Root: t.TempDir(), LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Shutdown("test")
	client, err := connect(rt.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, alias := range []string{"pkg", "package"} {
		if err := run(ctx, client, alias, []string{"list"}); err != nil {
			t.Fatal(err)
		}
		if err := run(ctx, client, alias, []string{"rollback"}); err == nil {
			t.Fatal("CLI accepted missing app ID")
		}
	}
}
