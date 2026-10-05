// Package security defines the environment's identity and capability
// primitives: users and permission tokens checked at IPC handler
// boundaries. It is deliberately tiny. It provides logical isolation
// only; see docs/security.md for exactly what is and is not protected.
package security

import (
	"sort"
	"sync"

	"gostalgia/sdk"
)

// User is an environment-level identity. Users exist independently of the
// host OS's user accounts.
type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Well-known capabilities. Applications declare the ones they need in
// their manifest; the runtime grants them to the application's call
// context, and system services check them before acting.
const (
	CapIPC       = sdk.CapIPC
	CapFileRead  = sdk.CapFileRead
	CapFileWrite = sdk.CapFileWrite
	CapProcList  = sdk.CapProcList
	CapProcStop  = sdk.CapProcStop
	CapAppList   = sdk.CapAppList
	CapAppLaunch = sdk.CapAppLaunch
	CapShutdown  = sdk.CapShutdown
	CapAdmin     = "admin"
)

// Capabilities is a concurrency-safe permission set.
type Capabilities struct {
	mu   sync.RWMutex
	caps map[string]bool
}

func NewCapabilities(capabilities ...string) *Capabilities {
	c := &Capabilities{caps: make(map[string]bool, len(capabilities))}
	for _, name := range capabilities {
		c.caps[name] = true
	}
	return c
}

func (c *Capabilities) Has(capability string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.caps[capability]
}

func (c *Capabilities) Grant(capabilities ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, name := range capabilities {
		c.caps[name] = true
	}
}

// List returns the granted capabilities, sorted.
func (c *Capabilities) List() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.caps))
	for name := range c.caps {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// AdminCapabilities returns the full capability set granted to trusted
// local control clients (gctl) and to the runtime's own in-process
// calls. Applications never receive this set; they get what their
// manifest declares.
func AdminCapabilities() *Capabilities {
	return NewCapabilities(
		CapIPC, CapFileRead, CapFileWrite,
		CapProcList, CapProcStop,
		CapAppList, CapAppLaunch,
		CapShutdown, CapAdmin,
	)
}
