//go:build unix

package platform

import (
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// ListenIPC opens the environment's local IPC listener. Unix-like hosts
// use a domain socket whose path is derived from the environment root.
// The path is kept short deliberately: domain socket paths are limited to
// about 104 bytes on macOS, and environment roots can live deep in temp
// or home directories.
func ListenIPC(root string) (net.Listener, string, error) {
	sum := sha1.Sum([]byte(root))
	path := filepath.Join(os.TempDir(), fmt.Sprintf("gostalgia-%x.sock", sum[:5]))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, "", err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", fmt.Errorf("platform: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, "", err
	}
	return ln, "unix://" + path, nil
}

// ListenChildIPC creates a dedicated IPC socket for a child application process.
// The socket is created in os.TempDir() with a short name and 0600 permissions.
func ListenChildIPC(appID string) (net.Listener, string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return nil, "", fmt.Errorf("platform: rand: %w", err)
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("gs-app-%x.sock", b))
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", fmt.Errorf("platform: listen child unix %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		_ = os.Remove(path)
		return nil, "", err
	}
	return ln, "unix://" + path, nil
}
