package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"

	"gostalgia/internal/security"
	"gostalgia/platform"
)

type mockNetAdapter struct {
	lastReq *http.Request
	body    []byte
	resp    *http.Response
	err     error
}

func (m *mockNetAdapter) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return nil, nil
}

func (m *mockNetAdapter) Do(req *http.Request) (*http.Response, error) {
	m.lastReq = req
	if req.Body != nil {
		m.body, _ = io.ReadAll(req.Body)
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func TestNetService_FetchAndPolicyEnforcement(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	capsNone := security.NewCapabilities()
	capsNet := security.NewCapabilities(security.CapIPC, security.CapNetEgress)

	// 1. Fetch without CapNetEgress fails
	res := env.call(ctx, capsNone, "net/fetch", map[string]string{
		"url": "https://api.example.com/data",
	})
	if res.OK {
		t.Fatal("expected net/fetch without CapNetEgress to fail")
	}

	// 2. Fetch fails when network disabled in policy
	policy := env.ctx.Policy.Get()
	policy.Network.Enabled = false
	env.ctx.Policy.Set(policy)

	res = env.call(ctx, capsNet, "net/fetch", map[string]string{
		"url": "https://api.example.com/data",
	})
	if res.OK {
		t.Fatal("expected net/fetch to fail when Network policy is disabled")
	}

	// 3. Enable network in policy with allowed host
	policy.Network.Enabled = true
	policy.Network.AllowedHosts = []string{"api.example.com"}
	policy.Network.AllowInsecure = false
	env.ctx.Policy.Set(policy)

	// Insecure http:// should fail
	res = env.call(ctx, capsNet, "net/fetch", map[string]string{
		"url": "http://api.example.com/data",
	})
	if res.OK {
		t.Fatal("expected http:// request to fail when AllowInsecure is false")
	}

	// Unallowed host should fail
	res = env.call(ctx, capsNet, "net/fetch", map[string]string{
		"url": "https://evil.com/data",
	})
	if res.OK {
		t.Fatal("expected request to unallowed host to fail")
	}

	// 4. Setup mock adapter and call allowed HTTPS endpoint
	origAdapter := platform.GetNetworkAdapter()
	defer platform.SetNetworkAdapter(origAdapter)

	respBody := []byte(`{"result":"success"}`)
	mock := &mockNetAdapter{
		resp: &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(bytes.NewReader(respBody)),
		},
	}
	platform.SetNetworkAdapter(mock)

	reqPayload := base64.StdEncoding.EncodeToString([]byte(`{"query":"test"}`))
	res = env.call(ctx, capsNet, "net/fetch", map[string]any{
		"url":         "https://api.example.com/data",
		"method":      "POST",
		"body_base64": reqPayload,
		"headers": map[string]string{
			"X-Custom-Header": "custom-val",
		},
	})
	if !res.OK {
		t.Fatalf("net/fetch failed: %s", res.Error)
	}

	var fetchOut struct {
		Status     int                 `json:"status"`
		StatusText string              `json:"status_text"`
		Headers    map[string][]string `json:"headers"`
		DataBase64 string              `json:"data_base64"`
		Size       int                 `json:"size"`
	}
	must(t, json.Unmarshal(res.Data, &fetchOut))
	if fetchOut.Status != 200 {
		t.Fatalf("expected status 200, got %d", fetchOut.Status)
	}
	if string(mock.body) != `{"query":"test"}` {
		t.Fatalf("expected request body to match, got %q", string(mock.body))
	}
	if mock.lastReq.Header.Get("X-Custom-Header") != "custom-val" {
		t.Fatalf("expected header X-Custom-Header=custom-val, got %q", mock.lastReq.Header.Get("X-Custom-Header"))
	}
	dataBytes, _ := base64.StdEncoding.DecodeString(fetchOut.DataBase64)
	if string(dataBytes) != `{"result":"success"}` {
		t.Fatalf("expected response body %q, got %q", `{"result":"success"}`, string(dataBytes))
	}

	// 5. Test net/status
	res = env.call(ctx, capsNet, "net/status", nil)
	if !res.OK {
		t.Fatalf("net/status failed: %s", res.Error)
	}
	var statusOut struct {
		PolicyEnabled bool     `json:"policy_enabled"`
		AllowedHosts  []string `json:"allowed_hosts"`
	}
	must(t, json.Unmarshal(res.Data, &statusOut))
	if !statusOut.PolicyEnabled || len(statusOut.AllowedHosts) != 1 {
		t.Fatalf("unexpected net/status output: %+v", statusOut)
	}
}

func TestSysService_PolicyGetAndUpdate(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	operator := security.OperatorPrincipal(security.User{Name: "admin"})
	appPrinc := security.AppPrincipal("com.test.app", 10, "sess-1", security.User{Name: "guest"})
	capsAdmin := security.AdminCapabilities()
	capsNone := security.NewCapabilities()
	capsIPC := security.NewCapabilities(security.CapIPC)

	// 1. sys/policy inspect
	res := env.call(ctx, capsIPC, "sys/policy", nil)
	if !res.OK {
		t.Fatalf("sys/policy failed: %s", res.Error)
	}
	var pol security.OperatorPolicy
	must(t, json.Unmarshal(res.Data, &pol))
	if pol.Clipboard.Enabled || pol.HostFS.Enabled || pol.Network.Enabled {
		t.Fatal("expected default policy to have all opt-ins disabled")
	}

	// 2. sys/policy/update by non-operator fails
	pol.Clipboard.Enabled = true
	res = env.callAs(ctx, appPrinc, capsNone, "sys/policy/update", pol)
	if res.OK {
		t.Fatal("expected non-operator sys/policy/update to fail")
	}

	// 3. sys/policy/update by operator succeeds
	res = env.callAs(ctx, operator, capsAdmin, "sys/policy/update", pol)
	if !res.OK {
		t.Fatalf("operator sys/policy/update failed: %s", res.Error)
	}

	// Verify updated
	res = env.call(ctx, capsIPC, "sys/policy", nil)
	if !res.OK {
		t.Fatalf("sys/policy failed: %s", res.Error)
	}
	var polAfter security.OperatorPolicy
	must(t, json.Unmarshal(res.Data, &polAfter))
	if !polAfter.Clipboard.Enabled {
		t.Fatal("expected policy to reflect updated clipboard.enabled=true")
	}
}
