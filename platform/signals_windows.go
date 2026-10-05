//go:build windows

package platform

import "os"

// ShutdownSignals returns the host signals that request a graceful
// environment shutdown on Windows. Go on Windows delivers only
// os.Interrupt (Ctrl-C and console close); syscall.SIGTERM is defined but
// never delivered, so requesting it there is a silent no-op — the reason
// this set lives behind a build tag. Other termination paths (taskkill /f,
// job-object teardown) reach the process as no signal at all; that gap is
// documented in docs/platform.md and is scheduled for the platform-parity
// milestone.
func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
