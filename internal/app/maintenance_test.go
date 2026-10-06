package app

import (
	"context"
	"testing"
	"time"

	"gostalgia/sdk"
)

func TestMaintenanceBlocksLaunchAndRevokesCredentials(t *testing.T) {
	m, _ := newTestManager(t)
	defer m.procs.Shutdown(time.Second)
	proc, err := m.Launch(context.Background(), fakeManifest.ID)
	must(t, err)
	token, ok := m.AppToken(fakeManifest.ID)
	if !ok {
		t.Fatal("missing token")
	}
	release, err := m.BeginMaintenance(fakeManifest.ID, time.Second)
	must(t, err)
	defer release()
	select {
	case <-proc.Done():
	default:
		t.Fatal("maintenance did not stop process")
	}
	if _, _, err := m.tokens.Authenticate(token); err == nil {
		t.Fatal("maintenance did not revoke credentials")
	}
	if _, err := m.Launch(context.Background(), fakeManifest.ID); err == nil {
		t.Fatal("maintenance allowed launch")
	}
	if _, err := m.BeginMaintenance(fakeManifest.ID, time.Second); err == nil {
		t.Fatal("overlapping maintenance allowed")
	}
	release()
	proc, err = m.Launch(context.Background(), fakeManifest.ID)
	must(t, err)
	must(t, m.Stop(fakeManifest.ID, time.Second))
	<-proc.Done()
}

type initializingApp struct{ entered, proceed chan struct{} }

func (a *initializingApp) Init(*sdk.Context) error     { close(a.entered); <-a.proceed; return nil }
func (*initializingApp) Run(ctx context.Context) error { <-ctx.Done(); return nil }
func (*initializingApp) Stop(context.Context) error    { return nil }

func TestMaintenanceRejectsInitializingApplication(t *testing.T) {
	m, _ := newTestManager(t)
	defer m.procs.Shutdown(time.Second)
	a := &initializingApp{entered: make(chan struct{}), proceed: make(chan struct{})}
	man := Manifest{ID: "com.test.initializing", Name: "Initializing", Version: "1.0.0", Entrypoint: "initializing"}
	must(t, m.reg.RegisterBuiltin(man, func() (Instance, error) { return a, nil }))
	launched := make(chan error, 1)
	go func() { _, err := m.Launch(context.Background(), man.ID); launched <- err }()
	<-a.entered
	release, err := m.BeginMaintenance(man.ID, time.Second)
	if err == nil {
		release()
		t.Error("maintenance accepted initializing app")
	}
	close(a.proceed)
	must(t, <-launched)
	must(t, m.Stop(man.ID, time.Second))
}
