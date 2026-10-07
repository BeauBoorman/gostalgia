package platform

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// The default adapter's CheckRedirect must invoke the gate carried by the
// request context on every hop, so an HTTPS origin cannot be bounced down to
// plaintext HTTP.
func TestDefaultAdapterRedirectCheckBlocksDowngrade(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "downgraded")
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer secure.Close()

	d := newDefaultNetworkAdapter()
	// Trust the test server's certificate; only the redirect gate is under test.
	d.client.Transport = secure.Client().Transport

	req, err := http.NewRequest("GET", secure.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithRedirectCheck(req.Context(), func(u *url.URL) error {
		if u.Scheme != "https" {
			return fmt.Errorf("refusing insecure redirect to %q", u)
		}
		return nil
	})
	resp, err := d.Do(req.WithContext(ctx))
	if err == nil {
		resp.Body.Close()
		t.Fatal("https->http redirect was followed")
	}
	if !strings.Contains(err.Error(), "refusing insecure redirect") {
		t.Fatalf("error = %v, want redirect gate denial", err)
	}
}

// Requests without a gate in context still observe the standard redirect cap.
func TestDefaultAdapterRedirectCapWithoutCheck(t *testing.T) {
	var selfURL string
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, selfURL, http.StatusFound)
	}))
	defer loop.Close()
	selfURL = loop.URL + "/again"

	d := newDefaultNetworkAdapter()
	resp, err := d.Do(mustGet(t, loop.URL))
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect loop was followed indefinitely")
	}
	if !strings.Contains(err.Error(), "stopped after") {
		t.Fatalf("error = %v, want redirect cap error", err)
	}
}

func mustGet(t *testing.T, rawurl string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("GET", rawurl, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
