//go:build windows

package platform

import (
	"os"
	"testing"
)

func TestShutdownSignals(t *testing.T) {
	got := ShutdownSignals()
	// Windows delivers only os.Interrupt; SIGTERM is defined but never
	// delivered, so it must not be requested.
	want := []os.Signal{os.Interrupt}
	if len(got) != len(want) {
		t.Fatalf("ShutdownSignals() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ShutdownSignals()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
