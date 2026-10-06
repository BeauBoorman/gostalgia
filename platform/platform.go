// Package platform isolates the host-specific integration points of the
// environment: the small set of things the standard library does not
// already abstract, such as the local IPC listener mechanism. Everything
// here must stay thin; if a file grows, the concern probably belongs in
// the subsystem it serves.
package platform

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// DialIPC connects to a local environment IPC endpoint. Endpoints are
// scheme-based URIs produced by ListenIPC: "unix://<path>" on macOS/Linux
// and "tcp://<host:port>" on Windows.
func DialIPC(endpoint string) (net.Conn, error) {
	switch {
	case strings.HasPrefix(endpoint, "unix://"):
		return net.Dial("unix", strings.TrimPrefix(endpoint, "unix://"))
	case strings.HasPrefix(endpoint, "tcp://"):
		return net.Dial("tcp", strings.TrimPrefix(endpoint, "tcp://"))
	default:
		return nil, fmt.Errorf("platform: unknown endpoint scheme %q", endpoint)
	}
}

// RemoveChildSocket cleans up a child socket file if the endpoint is a Unix domain socket.
func RemoveChildSocket(endpoint string) {
	if strings.HasPrefix(endpoint, "unix://") {
		_ = os.Remove(strings.TrimPrefix(endpoint, "unix://"))
	}
}
