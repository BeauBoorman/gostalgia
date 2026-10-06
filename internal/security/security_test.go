package security

import (
	"reflect"
	"testing"
)

func TestCapabilitiesSetSemantics(t *testing.T) {
	caps := NewCapabilities(CapFileRead)
	if !caps.Has(CapFileRead) {
		t.Error("granted capability reported missing")
	}
	if caps.Has(CapFileWrite) {
		t.Error("ungranted capability reported present")
	}
	if caps.Has(CapAdmin) {
		t.Error("empty-set capability reported present")
	}

	caps.Grant(CapFileWrite, CapFileRead) // duplicate grant is a no-op
	if !caps.Has(CapFileWrite) {
		t.Error("granted capability reported missing after Grant")
	}
	if got, want := caps.List(), []string{CapFileRead, CapFileWrite}; !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %v, want sorted %v", got, want)
	}
}

func TestAdminCapabilitiesCoversWellKnownSet(t *testing.T) {
	admin := AdminCapabilities()
	for _, cap := range []string{
		CapIPC, CapFileRead, CapFileWrite,
		CapProcList, CapProcStop,
		CapAppList, CapAppLaunch,
		CapShutdown, CapAdmin,
	} {
		if !admin.Has(cap) {
			t.Errorf("admin set is missing %q", cap)
		}
	}
}

func TestCapabilitiesAreConcurrencySafe(t *testing.T) {
	caps := NewCapabilities()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			caps.Grant("cap")
		}
	}()
	for i := 0; i < 200; i++ {
		_ = caps.Has("cap")
		_ = caps.List()
	}
	<-done
	if !caps.Has("cap") {
		t.Fatal("granted capability missing after concurrent grants")
	}
}

func TestTokenStoreOperatorLifecycle(t *testing.T) {
	ts := NewTokenStore()
	user := User{ID: "u-op", Name: "admin"}

	if err := ts.RegisterOperator("op-tok-123", user); err != nil {
		t.Fatalf("RegisterOperator: %v", err)
	}
	if err := ts.RegisterOperator("op-tok-123", user); err == nil {
		t.Error("duplicate operator token allowed, want error")
	}

	p, caps, err := ts.Authenticate("op-tok-123")
	if err != nil {
		t.Fatalf("Authenticate operator: %v", err)
	}
	if !p.IsOperator() || p.Kind != PrincipalKindOperator {
		t.Errorf("principal = %+v, want operator", p)
	}
	if !caps.Has(CapAdmin) || !caps.Has(CapShutdown) {
		t.Errorf("operator caps missing admin: %v", caps.List())
	}
	if err := ts.Validate("op-tok-123"); err != nil {
		t.Errorf("Validate operator: %v", err)
	}

	ts.Revoke("op-tok-123")
	if err := ts.Validate("op-tok-123"); err == nil {
		t.Error("revoked operator token validated, want error")
	}
	if _, _, err := ts.Authenticate("op-tok-123"); err == nil {
		t.Error("revoked operator token authenticated, want error")
	}
}

func TestTokenStoreAppLifecycleAndRevocation(t *testing.T) {
	ts := NewTokenStore()
	user := User{ID: "u-guest", Name: "guest"}

	tok1, err := ts.IssueAppToken("com.example.notes", 10, "sess-1", user, CapIPC, CapFileRead)
	if err != nil {
		t.Fatalf("IssueAppToken: %v", err)
	}
	if tok1 == "" {
		t.Fatal("issued token is empty")
	}

	p, caps, err := ts.Authenticate(tok1)
	if err != nil {
		t.Fatalf("Authenticate app: %v", err)
	}
	if !p.IsApp() || p.AppID != "com.example.notes" || p.ProcessID != 10 || p.SessionID != "sess-1" {
		t.Errorf("app principal = %+v", p)
	}
	if !caps.Has(CapIPC) || !caps.Has(CapFileRead) {
		t.Errorf("app missing granted capabilities: %v", caps.List())
	}
	if caps.Has(CapFileWrite) || caps.Has(CapShutdown) || caps.Has(CapAdmin) {
		t.Errorf("app granted unrequested capabilities: %v", caps.List())
	}

	// Revoke by process ID
	ts.RevokeProcess(10)
	if err := ts.Validate(tok1); err == nil {
		t.Error("Validate after RevokeProcess succeeded, want error")
	}
	if _, _, err := ts.Authenticate(tok1); err == nil {
		t.Error("Authenticate after RevokeProcess succeeded, want error")
	}

	// Issue another token for same app and revoke by App ID
	tok2, err := ts.IssueAppToken("com.example.notes", 0, "sess-1", user, CapIPC)
	if err != nil {
		t.Fatalf("IssueAppToken 2: %v", err)
	}
	ts.BindProcess(tok2, 20)
	if p, _, err := ts.Authenticate(tok2); err != nil || p.ProcessID != 20 {
		t.Fatalf("BindProcess failed: p=%+v, err=%v", p, err)
	}

	ts.RevokeApp("com.example.notes")
	if err := ts.Validate(tok2); err == nil {
		t.Error("Validate after RevokeApp succeeded, want error")
	}
}

func TestTokenStoreUnknownToken(t *testing.T) {
	ts := NewTokenStore()
	if err := ts.Validate("non-existent"); err == nil {
		t.Error("Validate unknown token succeeded, want error")
	}
	if _, _, err := ts.Authenticate("non-existent"); err == nil {
		t.Error("Authenticate unknown token succeeded, want error")
	}
}
