//go:build unix

package platform

import (
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
