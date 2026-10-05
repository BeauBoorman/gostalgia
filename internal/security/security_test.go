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
