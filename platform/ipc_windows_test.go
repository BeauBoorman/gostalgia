//go:build windows

package platform

import (
	"strings"
	"testing"
	"time"
)

// TestListenIPCWindowsLoopbackTCP covers the Windows TCP path: the
// endpoint is a tcp:// loopback URL and a client can dial it and exchange
// a byte. It runs only on Windows CI (see issue #9: the TCP path must not
// stay compile-only).
func TestListenIPCWindowsLoopbackTCP(t *testing.T) {
	ln, endpoint, err := ListenIPC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	if !strings.HasPrefix(endpoint, "tcp://") {
		t.Fatalf("endpoint = %q, want tcp:// scheme", endpoint)
	}
	if strings.Contains(endpoint, "0.0.0.0") {
		t.Fatalf("endpoint = %q, want loopback only", endpoint)
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
		t.Fatalf("round trip through loopback TCP: %v", err)
	}
}
