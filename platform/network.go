package platform

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
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

// redirectCheckKey carries a per-request egress gate consulted on every
// redirect hop. Redirected requests inherit the originating request's
// context, so a gate attached once applies to the whole redirect chain.
type redirectCheckKey struct{}

// ContextWithRedirectCheck attaches an egress policy gate to ctx. The
// default NetworkAdapter validates every redirect target against check and
// refuses the hop when check returns an error. Contexts without a gate keep
// only the standard 10-redirect cap.
func ContextWithRedirectCheck(ctx context.Context, check func(*url.URL) error) context.Context {
	return context.WithValue(ctx, redirectCheckKey{}, check)
}

// checkRedirect is the shared client's redirect gate: it runs the
// context-carried egress check on each hop, then applies the standard
// 10-redirect cap that a nil CheckRedirect would have provided.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if check, ok := req.Context().Value(redirectCheckKey{}).(func(*url.URL) error); ok && check != nil {
		if err := check(req.URL); err != nil {
			return err
		}
	}
	if len(via) >= 10 {
		return errors.New("net: stopped after 10 redirects")
	}
	return nil
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
		Transport:     transport,
		Timeout:       30 * time.Second,
		CheckRedirect: checkRedirect,
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
