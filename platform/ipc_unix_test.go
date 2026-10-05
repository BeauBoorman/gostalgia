//go:build unix

package platform

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestListenIPCUnixSocketPath covers the unix domain socket path: the
// endpoint is a unix:// URL, the socket file exists with 0600 permissions,
// and a client can dial it and exchange a byte.
func TestListenIPCUnixSocketPath(t *testing.T) {
	ln, endpoint, err := ListenIPC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	path := strings.TrimPrefix(endpoint, "unix://")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("socket file %s: %v", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is not a socket (mode %s)", path, info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perms = %o, want 600 (local trust only)", perm)
	}

	go func() {
		conn, err := ln.Accept()
		if err == nil {
			conn.Write([]byte{0x42})
			conn.Close()
		}
	}()
	conn, err := DialIPC(endpoint)
	if err != nil {
		t.Fatalf("DialIPC(%s): %v", endpoint, err)
	}
	defer conn.Close()
	buf := make([]byte, 1)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("round trip through unix socket: %v", err)
	}
}

// TestListenIPCUnixReplacesStaleSocket: a leftover socket file from a dead
// instance must not block a new listener.
func TestListenIPCUnixReplacesStaleSocket(t *testing.T) {
	root := t.TempDir()
	first, endpoint, err := ListenIPC(root)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	second, endpoint2, err := ListenIPC(root)
	if err != nil {
		t.Fatalf("re-listen on a stale socket path failed: %v", err)
	}
	defer second.Close()
	if endpoint2 != endpoint {
		t.Errorf("endpoint = %q, want the same derived path %q", endpoint2, endpoint)
	}
}
