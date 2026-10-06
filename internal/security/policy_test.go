package security_test

import (
	"testing"

	"gostalgia/internal/security"
)

func TestDefaultOperatorPolicy(t *testing.T) {
	p := security.DefaultOperatorPolicy()

	if p.Clipboard.Enabled {
		t.Errorf("expected clipboard disabled by default")
	}
	if p.HostFS.Enabled {
		t.Errorf("expected hostfs disabled by default")
	}
	if p.Network.Enabled {
		t.Errorf("expected network disabled by default")
	}

	if err := p.CheckClipboard(false); err == nil {
		t.Errorf("expected clipboard read error when disabled")
	}
	if err := p.CheckClipboard(true); err == nil {
		t.Errorf("expected clipboard write error when disabled")
	}

	if err := p.CheckHostFS("/tmp/foo", false); err == nil {
		t.Errorf("expected hostfs read error when disabled")
	}
	if err := p.CheckHostFS("/tmp/foo", true); err == nil {
		t.Errorf("expected hostfs write error when disabled")
	}

	if err := p.CheckNetwork("https://example.com"); err == nil {
		t.Errorf("expected network error when disabled")
	}
}

func TestClipboardPolicy(t *testing.T) {
	p := security.DefaultOperatorPolicy()
	p.Clipboard.Enabled = true
	p.Clipboard.AllowHostRead = true
	p.Clipboard.AllowHostWrite = false

	if err := p.CheckClipboard(false); err != nil {
		t.Fatalf("expected clipboard read allowed: %v", err)
	}
	if err := p.CheckClipboard(true); err == nil {
		t.Fatalf("expected clipboard write denied")
	}

	p.Clipboard.AllowHostWrite = true
	if err := p.CheckClipboard(true); err != nil {
		t.Fatalf("expected clipboard write allowed: %v", err)
	}
}

func TestHostFSPolicy(t *testing.T) {
	p := security.DefaultOperatorPolicy()
	p.HostFS.Enabled = true
	p.HostFS.AllowRead = true
	p.HostFS.AllowWrite = false
	p.HostFS.AllowedPaths = []string{"/tmp/allowed"}

	if err := p.CheckHostFS("/tmp/allowed/sub", false); err != nil {
		t.Fatalf("expected read allowed: %v", err)
	}
	if err := p.CheckHostFS("/tmp/allowed/sub", true); err == nil {
		t.Fatalf("expected write denied")
	}
	if err := p.CheckHostFS("/etc/passwd", false); err == nil {
		t.Fatalf("expected path outside allowed paths denied")
	}
}

func TestNetworkPolicy(t *testing.T) {
	p := security.DefaultOperatorPolicy()
	p.Network.Enabled = true
	p.Network.AllowedHosts = []string{"example.com", "*.google.com"}
	p.Network.BlockedHosts = []string{"evil.google.com"}
	p.Network.AllowedPorts = []int{443, 8443}

	// Plain HTTP blocked because allow_insecure is false
	if err := p.CheckNetwork("http://example.com"); err == nil {
		t.Fatalf("expected http blocked when allow_insecure is false")
	}

	// HTTPS to example.com:443 allowed
	if err := p.CheckNetwork("https://example.com"); err != nil {
		t.Fatalf("expected allowed: %v", err)
	}

	// Wildcard subdomain allowed
	if err := p.CheckNetwork("https://maps.google.com"); err != nil {
		t.Fatalf("expected allowed: %v", err)
	}

	// Blocked host denied
	if err := p.CheckNetwork("https://evil.google.com"); err == nil {
		t.Fatalf("expected blocked host denied")
	}

	// Unlisted host denied
	if err := p.CheckNetwork("https://other.com"); err == nil {
		t.Fatalf("expected unlisted host denied")
	}

	// Port not in allowed list denied
	if err := p.CheckNetwork("https://example.com:8080"); err == nil {
		t.Fatalf("expected unlisted port denied")
	}
}

func TestPolicyStore(t *testing.T) {
	store := security.NewPolicyStore(security.DefaultOperatorPolicy())
	if store.Get().Clipboard.Enabled {
		t.Fatalf("expected disabled")
	}

	store.Update(func(p *security.OperatorPolicy) {
		p.Clipboard.Enabled = true
	})
	if !store.Get().Clipboard.Enabled {
		t.Fatalf("expected updated")
	}
}
