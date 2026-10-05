//go:build unix

package services

// End-to-end regression for issue #16: fs/list over the real IPC server
// against a host root containing a FIFO. The handler used to block
// forever inside HostFS.ReadDir, which held Server.Close() — and with it
// the whole shutdown sequence — open indefinitely. Unix-only because
// creating a FIFO needs syscall.Mkfifo.

import (
	"context"
	"net"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"gostalgia/internal/ipc"
)

func TestFSListOverSocketWithFIFOCompletes(t *testing.T) {
	env := newTestEnv(t)
	// newTestEnv's host-backed root lives at <root>/vfs.
	must(t, syscall.Mkfifo(filepath.Join(env.ctx.Root, "vfs", "fifo"), 0o644))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	srv := ipc.NewServer(ln, env.router, "test-token", env.ctx.Log)
	go srv.Serve()

	conn, err := net.Dial("tcp", ln.Addr().String())
	must(t, err)
	client, err := ipc.NewClient(conn, "test-token")
	must(t, err)
	t.Cleanup(func() { client.Close() })

	type fsEntry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
	}
	type listResult struct {
		entries []fsEntry
		err     error
	}
	list := make(chan listResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var out struct {
			Entries []fsEntry `json:"entries"`
		}
		err := client.Call(ctx, "fs/list", map[string]string{"path": "/"}, &out)
		list <- listResult{out.Entries, err}
	}()

	// Give the handler time to reach the VFS; with the regression
	// present it is now stuck inside HostFS.ReadDir.
	time.Sleep(100 * time.Millisecond)

	// Server.Close waits for in-flight handlers (issue #2), so with the
	// regression this blocks forever. It must complete.
	closed := make(chan error, 1)
	go func() { closed <- srv.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Server.Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ipc.Server.Close blocked >5s while a fs/list handler was in flight")
	}

	// The listing itself must answer and still include the FIFO.
	select {
	case r := <-list:
		if r.err != nil {
			t.Fatalf("fs/list: %v", r.err)
		}
		found := false
		for _, e := range r.entries {
			if e.Name == "fifo" && !e.IsDir {
				found = true
			}
		}
		if !found {
			t.Errorf("fs/list did not report the fifo entry: %+v", r.entries)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fs/list blocked >5s on a FIFO entry")
	}
}
