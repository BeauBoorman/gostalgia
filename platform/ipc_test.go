package platform

import (
	"testing"
)

func TestDialIPCRejectsUnknownScheme(t *testing.T) {
	for _, endpoint := range []string{"", "http://127.0.0.1:9", "pipe:gostalgia", "unix:/no-scheme-slash"} {
		conn, err := DialIPC(endpoint)
		if err == nil {
			conn.Close()
			t.Errorf("DialIPC(%q) succeeded, want unknown-scheme error", endpoint)
		}
		if conn != nil {
			t.Errorf("DialIPC(%q) returned a conn alongside an error", endpoint)
		}
	}
}
