//go:build unix

package platform

import (
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
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

// ChildIPC returns a pre-connected anonymous IPC channel for a sandboxed
// child process. parent is the runtime's end of the channel; child is an
// open file that must be passed to the child via exec.Cmd.ExtraFiles (it
// becomes fd 3 there). Unlike a socket-path listener, the channel exists
// before spawn and needs no filesystem endpoint, so a confined child can
// reach the runtime without any network-outbound capability — a Seatbelt or
// namespace policy that denies socket connect() still allows I/O on the
// inherited descriptor.
//
// The caller should close child once exec.Cmd.Start has succeeded: the child
// then owns the only peer copy, and its exit yields EOF on parent.
func ChildIPC() (parent net.Conn, child *os.File, err error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("platform: socketpair: %w", err)
	}
	parentFile := os.NewFile(uintptr(fds[0]), "gostalgia-child-ipc")
	child = os.NewFile(uintptr(fds[1]), "gostalgia-child-ipc-peer")
	if parentFile == nil || child == nil {
		_ = parentFile.Close()
		if child != nil {
			_ = child.Close()
		} else {
			_ = syscall.Close(fds[1])
		}
		return nil, nil, fmt.Errorf("platform: socketpair: invalid fd")
	}
	// net.FileConn dups the descriptor; close the raw parent fd afterwards.
	parent, err = net.FileConn(parentFile)
	_ = parentFile.Close()
	if err != nil {
		_ = child.Close()
		return nil, nil, fmt.Errorf("platform: socketpair fileconn: %w", err)
	}
	return parent, child, nil
}
