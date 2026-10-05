//go:build unix

package platform

import (
	"os"
	"syscall"
)

// ShutdownSignals returns the host signals that request a graceful
// environment shutdown on unix-like hosts: Ctrl-C and the conventional
// kill TERM. Callers pass the result to os/signal.
func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
