package platform

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type mockNetworkAdapter struct {
	roundTrip func(req *http.Request) (*http.Response, error)
}

func (m *mockNetworkAdapter) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return nil, nil
}

func (m *mockNetworkAdapter) Do(req *http.Request) (*http.Response, error) {
	if m.roundTrip != nil {
		return m.roundTrip(req)
	}
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("mock response")),
	}, nil
}

func TestNetworkAdapterGetterSetter(t *testing.T) {
	orig := GetNetworkAdapter()
	defer SetNetworkAdapter(orig)

	mock := &mockNetworkAdapter{}
	// Set mock adapter
	SetNetworkAdapter(mock)
	if GetNetworkAdapter() != mock {
		t.Fatal("expected mock adapter to be active")
	}

	// Reset adapter (pass nil restores default)
	SetNetworkAdapter(nil)
	if GetNetworkAdapter() == nil {
		t.Fatal("expected non-nil default adapter")
	}
}
