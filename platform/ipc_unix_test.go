//go:build unix

package platform

import (
	"os"
	"os/exec"
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

// TestChildIPCHelper is the re-executed child for TestChildIPCRoundTrip. It
// reads a byte from the inherited IPC descriptor (fd 3) and replies on it —
// no connect() is ever performed, which is what makes this transport usable
// under a network-denying sandbox profile.
func TestChildIPCHelper(t *testing.T) {
	if os.Getenv("GOSTALGIA_TEST_IPC_CHILD") != "1" {
		return
	}
	f := os.NewFile(3, "ipc")
	buf := make([]byte, 4)
	if _, err := f.Read(buf); err != nil {
		os.Exit(2)
	}
	if _, err := f.Write([]byte("PONG")); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

// TestChildIPCRoundTrip verifies that a child spawned with the ChildIPC
// descriptor in ExtraFiles can exchange bytes with the parent — the core of
// the #83 fix (IPC without any unix-socket connect capability).
func TestChildIPCRoundTrip(t *testing.T) {
	conn, child, err := ChildIPC()
	if err != nil {
		t.Fatalf("ChildIPC: %v", err)
	}
	defer conn.Close()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=TestChildIPCHelper$")
	cmd.Env = append(os.Environ(), "GOSTALGIA_TEST_IPC_CHILD=1")
	cmd.ExtraFiles = []*os.File{child}
	if err := cmd.Start(); err != nil {
		child.Close()
		t.Fatalf("start helper: %v", err)
	}
	// Parent drops its copy so the child fully owns the peer end.
	child.Close()

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("PING")); err != nil {
		t.Fatalf("write to child: %v", err)
	}
	buf := make([]byte, 4)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read from child: %v", err)
	}
	if string(buf) != "PONG" {
		t.Fatalf("reply = %q, want PONG", buf)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}
}
