package platform

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

// NetworkAdapter abstracts network egress operations.
type NetworkAdapter interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	Do(req *http.Request) (*http.Response, error)
}

type defaultNetworkAdapter struct {
	client *http.Client
	dialer *net.Dialer
}

func newDefaultNetworkAdapter() *defaultNetworkAdapter {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		DialContext:         dialer.DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
	return &defaultNetworkAdapter{
		client: client,
		dialer: dialer,
	}
}

func (d *defaultNetworkAdapter) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.dialer.DialContext(ctx, network, address)
}

func (d *defaultNetworkAdapter) Do(req *http.Request) (*http.Response, error) {
	return d.client.Do(req)
}

var (
	netMu          sync.RWMutex
	currentNetwork NetworkAdapter = newDefaultNetworkAdapter()
)

// GetNetworkAdapter returns the active network adapter.
func GetNetworkAdapter() NetworkAdapter {
	netMu.RLock()
	defer netMu.RUnlock()
	return currentNetwork
}

// SetNetworkAdapter sets the active network adapter (useful for testing and mocking).
func SetNetworkAdapter(adapter NetworkAdapter) {
	netMu.Lock()
	defer netMu.Unlock()
	if adapter == nil {
		currentNetwork = newDefaultNetworkAdapter()
		return
	}
	currentNetwork = adapter
}
