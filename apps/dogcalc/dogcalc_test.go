package dogcalc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gostalgia/apps/calculator"
	"gostalgia/internal/app"
	"gostalgia/internal/events"
	"gostalgia/internal/ipc"
	"gostalgia/internal/process"
	"gostalgia/internal/security"
	"gostalgia/sdk"
)

// launchDog boots the real application through the real Manager, so its
// routes reach the router exactly as a launch publishes them.
func launchDog(t *testing.T) (*app.Manager, *ipc.Router) {
	t.Helper()
	return launchDogInstance(t, nil)
}

// launchDogInstance launches inst (or a fresh one from Factory when nil),
// keeping the instance reachable for the sound-grant test.
func launchDogInstance(t *testing.T, inst *Dog) (*app.Manager, *ipc.Router) {
	t.Helper()
	bus := events.NewBus()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := ipc.NewRouter()
	reg := app.NewRegistry()
	factory := Factory
	if inst != nil {
		factory = func() (sdk.Instance, error) { return inst, nil }
	}
	if err := reg.RegisterBuiltin(Manifest(), factory); err != nil {
		t.Fatal(err)
	}
	mgr := app.NewManager(reg, process.NewManager(bus, log), router, bus, log)
	if _, err := mgr.Launch(context.Background(), ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Stop(ID, 2*time.Second) })
	return mgr, router
}

// dispatch sends an IPC request with dogcalc's ipc capability.
func dispatch(t *testing.T, r *ipc.Router, id int64, method string, params string) ipc.Response {
	t.Helper()
	ctx := ipc.WithCapabilities(context.Background(), security.NewCapabilities(security.CapIPC))
	return r.Dispatch(ctx, ipc.Request{
		ID:     id,
		Method: "app/" + ID + "/" + method,
		Params: json.RawMessage(params),
	})
}

func mustView(t *testing.T, data json.RawMessage) sdk.View {
	t.Helper()
	var v sdk.View
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal view: %v", err)
	}
	if err := v.Validate(); err != nil {
		t.Fatalf("view validation: %v", err)
	}
	return v
}

// fetchView requests a snapshot at the negotiated version.
func fetchView(t *testing.T, r *ipc.Router, version int) sdk.View {
	t.Helper()
	resp := dispatch(t, r, 1, "view", fmt.Sprintf(`{"version":%d}`, version))
	if !resp.OK {
		t.Fatalf("view v%d failed: %s", version, resp.Error)
	}
	return mustView(t, resp.Data)
}

// cellPress returns a sender for pad keys. Each call dispatches either a
// version-2 grid press (action "press" + cell_id) or a version-1 labeled
// action, depending on the snapshot's negotiated version.
func cellPress(t *testing.T, r *ipc.Router, instance string, version int) func(string) sdk.View {
	t.Helper()
	seq := 0
	return func(key string) sdk.View {
		seq++
		p := sdk.ActionRequest{
			Version:   version,
			Instance:  instance,
			RequestID: fmt.Sprintf("r%d", seq),
		}
		if version >= 2 {
			p.Action = pressAction
			p.CellID = key
		} else {
			p.Action = key
		}
		req, _ := json.Marshal(p)
		resp := dispatch(t, r, int64(seq+10), "action", string(req))
		if !resp.OK {
			t.Fatalf("key %s failed: %s", key, resp.Error)
		}
		return mustView(t, resp.Data)
	}
}

func cellByID(t *testing.T, g *sdk.Grid, id string) sdk.Cell {
	t.Helper()
	for _, c := range g.Cells {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("grid has no cell %q", id)
	return sdk.Cell{}
}

func TestManifestValidity(t *testing.T) {
	m := Manifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest validation: %v", err)
	}
	if m.ID != ID {
		t.Errorf("id = %s, want %s", m.ID, ID)
	}
	if m.Entrypoint != "dogcalc" {
		t.Errorf("entrypoint = %s, want dogcalc", m.Entrypoint)
	}
	if !json.Valid(ManifestJSON()) {
		t.Error("ManifestJSON is not valid JSON")
	}
	if len(m.Permissions) != 2 || m.Permissions[0] != "ipc" || m.Permissions[1] != "sound" {
		t.Errorf("permissions = %v, want [ipc sound]", m.Permissions)
	}
}

// TestPadGridV2 checks the contract-v2 pad: every key is a dog, cells fill
// row-major with a short final row, and all declare the shared press action.
func TestPadGridV2(t *testing.T) {
	_, r := launchDog(t)
	v := fetchView(t, r, 2)

	if v.Version != 2 {
		t.Fatalf("version = %d, want 2", v.Version)
	}
	g := v.Grid
	if g == nil {
		t.Fatal("version-2 view has no grid")
	}
	if g.Columns != padColumns {
		t.Errorf("columns = %d, want %d", g.Columns, padColumns)
	}
	if len(g.Cells) != len(padOrder) {
		t.Fatalf("cells = %d, want %d", len(g.Cells), len(padOrder))
	}
	for i, id := range padOrder {
		c := g.Cells[i]
		if c.ID != id {
			t.Errorf("cell %d id = %q, want %q", i, c.ID, id)
		}
		if c.Action != pressAction {
			t.Errorf("cell %q action = %q, want %q", id, c.Action, pressAction)
		}
		if c.Label != keyLabels[id] {
			t.Errorf("cell %q label = %q, want %q", id, c.Label, keyLabels[id])
		}
	}
	if got := cellByID(t, g, "eq").Label; got != "BOOP =" {
		t.Errorf("eq label = %q, want BOOP =", got)
	}
	if got := cellByID(t, g, "clear").Label; got != "WAG C" {
		t.Errorf("clear label = %q, want WAG C", got)
	}
	// The grid is the whole pad: no top-level actions on version 2.
	if len(v.Actions) != 0 {
		t.Errorf("v2 actions = %v, want none", v.Actions)
	}
}

// TestV1Fallback checks the contract-v1 snapshot: no grid, the sixteen keys
// as labeled actions, still all dogs.
func TestV1Fallback(t *testing.T) {
	_, r := launchDog(t)
	v := fetchView(t, r, 1)

	if v.Version != 1 {
		t.Fatalf("version = %d, want 1", v.Version)
	}
	if v.Grid != nil {
		t.Fatal("version-1 snapshot leaked a grid")
	}
	if len(v.Actions) != 16 {
		t.Fatalf("v1 actions = %d, want 16", len(v.Actions))
	}
	labels := map[string]string{}
	for _, a := range v.Actions {
		labels[a.ID] = a.Label
	}
	for _, id := range v1Order {
		if labels[id] != keyLabels[id] {
			t.Errorf("v1 action %q label = %q, want %q", id, labels[id], keyLabels[id])
		}
	}
	if _, ok := labels["dot"]; ok {
		t.Error("v1 fallback exposes dot key past the 16-action bound")
	}
	if _, ok := labels["neg"]; ok {
		t.Error("v1 fallback exposes neg key past the 16-action bound")
	}
}

// TestGridArithmetic runs the Calculator acceptance core over cell presses:
// chained left-to-right ops and a correct result.
func TestGridArithmetic(t *testing.T) {
	_, r := launchDog(t)
	v := fetchView(t, r, 2)
	if v.Items[0].Detail != "0" {
		t.Errorf("initial display = %q, want 0", v.Items[0].Detail)
	}

	press := cellPress(t, r, v.Instance, 2)
	press("digit_1")
	press("add")
	press("digit_2")
	v = press("eq")
	if v.Items[0].Detail != "3" {
		t.Errorf("1+2 = %q, want 3", v.Items[0].Detail)
	}
	// Chained: + 3 =
	press("add")
	press("digit_3")
	v = press("eq")
	if v.Items[0].Detail != "6" {
		t.Errorf("3+3 = %q, want 6", v.Items[0].Detail)
	}
}

// TestV1Arithmetic runs the same arithmetic through the labeled-action
// fallback a version-1 caller gets.
func TestV1Arithmetic(t *testing.T) {
	_, r := launchDog(t)
	v := fetchView(t, r, 1)

	press := cellPress(t, r, v.Instance, 1)
	press("digit_4")
	press("mul")
	press("digit_2")
	v = press("eq")
	if v.Items[0].Detail != "8" {
		t.Errorf("4x2 = %q, want 8", v.Items[0].Detail)
	}
}

// TestDecimalAndNegateKeys exercises the two extra keys the grid affords
// over the 16-action v1 bound.
func TestDecimalAndNegateKeys(t *testing.T) {
	_, r := launchDog(t)
	v := fetchView(t, r, 2)
	press := cellPress(t, r, v.Instance, 2)

	// 1.5 + 2.5 = 4
	press("digit_1")
	v = press("dot")
	if !cellByID(t, v.Grid, "dot").Disabled {
		t.Error("dot key still enabled with a decimal point already entered")
	}
	v = press("digit_5")
	if v.Items[0].Detail != "1.5" {
		t.Fatalf("after 1.5 display = %q", v.Items[0].Detail)
	}
	v = press("add")
	if cellByID(t, v.Grid, "dot").Disabled {
		t.Error("dot key stayed disabled for the second operand")
	}
	press("digit_2")
	press("dot")
	press("digit_5")
	v = press("eq")
	if v.Items[0].Detail != "4" {
		t.Errorf("1.5+2.5 = %q, want 4", v.Items[0].Detail)
	}

	// Roll the sign on the result, then fetch a bigger bone: -4 + 8 = 4
	press("neg")
	v = press("add")
	if v.Items[0].Detail != "-4 +" {
		t.Fatalf("after neg+add display = %q", v.Items[0].Detail)
	}
	press("digit_8")
	v = press("eq")
	if v.Items[0].Detail != "4" {
		t.Errorf("-4+8 = %q, want 4", v.Items[0].Detail)
	}

	// Negate mid-entry: 3 ROLL => -3, then -3 - 8 = -11.
	press("clear")
	press("digit_3")
	v = press("neg")
	if v.Items[0].Detail != "-3" {
		t.Fatalf("after neg display = %q, want -3", v.Items[0].Detail)
	}
	press("sub")
	press("digit_8")
	v = press("eq")
	if v.Items[0].Detail != "-11" {
		t.Errorf("-3-8 = %q, want -11", v.Items[0].Detail)
	}
}

func TestDivideByZeroAndClear(t *testing.T) {
	_, r := launchDog(t)
	v := fetchView(t, r, 2)
	press := cellPress(t, r, v.Instance, 2)

	press("digit_1")
	press("div")
	press("digit_0")
	v = press("eq")
	if v.State != sdk.ViewError || !strings.Contains(v.Error, "divide by zero") {
		t.Errorf("divide by zero state = %q error = %q, want ViewError with divide-by-zero", v.State, v.Error)
	}

	v = press("clear")
	if v.State != sdk.ViewReady || v.Error != "" || v.Items[0].Detail != "0" {
		t.Errorf("after clear: state=%q error=%q detail=%q, want ready, no error, 0", v.State, v.Error, v.Items[0].Detail)
	}
}

// TestCellPressValidation: a grid press must name a real enabled cell, and
// action ids outside the pad are rejected at both versions.
func TestCellPressValidation(t *testing.T) {
	_, r := launchDog(t)
	v := fetchView(t, r, 2)

	for i, p := range []sdk.ActionRequest{
		// press without a cell_id is meaningless.
		{Version: 2, Instance: v.Instance, RequestID: "b1", Action: pressAction},
		// An unknown cell id is rejected.
		{Version: 2, Instance: v.Instance, RequestID: "b2", Action: pressAction, CellID: "digit_10"},
		// Pad key names are not top-level actions on v2.
		{Version: 2, Instance: v.Instance, RequestID: "b3", Action: "eq"},
		// A v1 request cannot reach the grid at all.
		{Version: 1, Instance: v.Instance, RequestID: "b4", Action: pressAction, CellID: "digit_1"},
	} {
		req, _ := json.Marshal(p)
		if resp := dispatch(t, r, int64(20+i), "action", string(req)); resp.OK {
			t.Errorf("invalid press dispatched: %+v", p)
		}
	}

	// v1 falls back to labeled actions only: dot/neg are unreachable there.
	v1 := fetchView(t, r, 1)
	for _, a := range []string{"dot", "neg"} {
		req, _ := json.Marshal(sdk.ActionRequest{
			Version: 1, Instance: v1.Instance, RequestID: "x" + a, Action: a,
		})
		if resp := dispatch(t, r, 30, "action", string(req)); resp.OK {
			t.Errorf("v1 action %q succeeded, want rejection past the 16-action bound", a)
		}
	}
}

// TestCalcRoute is the headless parity check: the same expression route the
// version-1 Calculator serves, evaluated by the same shared core.
func TestCalcRoute(t *testing.T) {
	_, r := launchDog(t)

	resp := dispatch(t, r, 1, "calc", `{"expr":"1+2+3"}`)
	if !resp.OK {
		t.Fatalf("calc failed: %s", resp.Error)
	}
	var out struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Result != "6" {
		t.Errorf("result = %s, want 6", out.Result)
	}
	// Left-to-right, matching the shared core exactly.
	resp = dispatch(t, r, 2, "calc", `{"expr":"2+3*4"}`)
	if !resp.OK {
		t.Fatalf("calc failed: %s", resp.Error)
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Result != "20" {
		t.Errorf("result = %s, want 20 (left-to-right)", out.Result)
	}
	for _, expr := range []string{`{"expr":"1/0"}`, `{"expr":"1++"}`, `{"expr":""}`} {
		if resp := dispatch(t, r, 3, "calc", expr); resp.OK {
			t.Errorf("calc(%s) succeeded, want error", expr)
		}
	}
}

// TestPadSharedWithCalculator pins the pad state machine to the same presses
// the version-1 Calculator performs, including the grid-only keys.
func TestPadSharedWithCalculator(t *testing.T) {
	p := calculator.NewPad()
	if got := p.Display(); got != "0" {
		t.Fatalf("fresh pad display = %q, want 0", got)
	}
	p.Digit(1)
	p.Dot()
	p.Digit(5)
	if got := p.Display(); got != "1.5" {
		t.Fatalf("display = %q, want 1.5", got)
	}
	if !p.HasDot() {
		t.Fatal("HasDot false after entering a decimal point")
	}
	p.Dot() // second dot is a no-op, not an error
	if got := p.Display(); got != "1.5" || p.Err() != "" {
		t.Fatalf("double dot: display = %q err = %q", got, p.Err())
	}
	p.Op("+")
	if p.HasDot() {
		t.Fatal("HasDot leaked into the second operand")
	}
	p.Digit(2)
	p.Dot()
	p.Digit(5)
	p.Eq()
	if got := p.Display(); got != "4" || p.Err() != "" {
		t.Fatalf("1.5+2.5: display = %q err = %q", got, p.Err())
	}
	p.Neg() // flips a computed result in place
	if got := p.Display(); got != "-4" {
		t.Fatalf("negated result = %q, want -4", got)
	}
	p.Digit(7) // a digit after a result starts a fresh entry
	if got := p.Display(); got != "7" {
		t.Fatalf("fresh entry = %q, want 7", got)
	}
	p.Neg()
	if got := p.Display(); got != "-7" {
		t.Fatalf("negated entry = %q, want -7", got)
	}
	p.Neg()
	p.Clear()
	p.Neg()
	if got := p.Display(); got != "-0" {
		t.Fatalf("negated zero = %q, want -0", got)
	}
	p.Digit(5)
	if got := p.Display(); got != "-5" {
		t.Fatalf("negated zero + 5 = %q, want -5", got)
	}
	p.Op("/")
	p.Digit(0)
	p.Eq()
	if p.Err() != "cannot divide by zero" {
		t.Fatalf("divide by zero err = %q", p.Err())
	}
	// A sticky error resets on the next press.
	p.Digit(9)
	if got := p.Display(); got != "9" || p.Err() != "" {
		t.Fatalf("post-error press: display = %q err = %q", got, p.Err())
	}
}

// TestBarkSilentWithoutGrant: with no sound capability or audio route, the
// equals press ships silently — no error, no outgoing call.
func TestBarkSilentWithoutGrant(t *testing.T) {
	d := &Dog{pad: calculator.NewPad()}
	_, r := launchDogInstance(t, d)

	var mu sync.Mutex
	calls := 0
	if err := r.Handle(soundRoute, func(context.Context, ipc.Request) (any, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}

	v := fetchView(t, r, 2)
	press := cellPress(t, r, v.Instance, 2)
	press("digit_1")
	press("add")
	press("digit_2")
	v = press("eq")
	if v.Items[0].Detail != "3" || v.State != sdk.ViewReady {
		t.Fatalf("eq view = %q state=%q, want 3/ready", v.Items[0].Detail, v.State)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("bark fired %d times without a sound grant, want 0", calls)
	}
	if d.sound != soundOff {
		t.Errorf("sound probe = %d, want latched off", d.sound)
	}
}

// TestBarkWithGrant: when the instance's grant reports the sound capability,
// booping equals calls the audio adapter once.
func TestBarkWithGrant(t *testing.T) {
	d := &Dog{pad: calculator.NewPad(), sound: soundOn}
	_, r := launchDogInstance(t, d)

	var mu sync.Mutex
	var sounds []string
	if err := r.Handle(soundRoute, func(_ context.Context, req ipc.Request) (any, error) {
		var p struct {
			Sound string `json:"sound"`
		}
		_ = json.Unmarshal(req.Params, &p)
		mu.Lock()
		sounds = append(sounds, p.Sound)
		mu.Unlock()
		return map[string]bool{"played": true}, nil
	}); err != nil {
		t.Fatal(err)
	}

	v := fetchView(t, r, 2)
	press := cellPress(t, r, v.Instance, 2)
	press("digit_2")
	press("add")
	press("digit_2")
	press("eq")
	press("digit_1") // non-eq keys never bark
	mu.Lock()
	defer mu.Unlock()
	if len(sounds) != 1 || sounds[0] != "bark" {
		t.Errorf("sounds = %v, want one bark", sounds)
	}
}

// TestBarkRouteMissing: the grant may exist without the adapter; a failed
// bark is swallowed and the equals press still reports the result.
func TestBarkRouteMissing(t *testing.T) {
	d := &Dog{pad: calculator.NewPad(), sound: soundOn}
	_, r := launchDogInstance(t, d)

	v := fetchView(t, r, 2)
	press := cellPress(t, r, v.Instance, 2)
	press("digit_1")
	press("add")
	press("digit_1")
	v = press("eq") // no sound/play handler registered: Call errors, swallowed
	if v.Items[0].Detail != "2" || v.State != sdk.ViewReady {
		t.Fatalf("eq view = %q state=%q, want 2/ready", v.Items[0].Detail, v.State)
	}
}

// TestBarkSkippedWhenHostUnsupported: the grant names sound but the status
// probe reports an unsupported host, so the equals press latches off and
// never spends the doomed sound/play call.
func TestBarkSkippedWhenHostUnsupported(t *testing.T) {
	d := &Dog{pad: calculator.NewPad()}
	_, r := launchDogInstance(t, d)

	if err := r.Handle("session/whoami", func(context.Context, ipc.Request) (any, error) {
		return map[string]any{"capabilities": []string{"ipc", "sound"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(soundStatusRoute, func(context.Context, ipc.Request) (any, error) {
		return map[string]any{"supported": false, "player": ""}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	plays := 0
	if err := r.Handle(soundRoute, func(context.Context, ipc.Request) (any, error) {
		mu.Lock()
		plays++
		mu.Unlock()
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}

	v := fetchView(t, r, 2)
	press := cellPress(t, r, v.Instance, 2)
	press("digit_1")
	v = press("eq")
	if v.Items[0].Detail != "1" || v.State != sdk.ViewReady {
		t.Fatalf("eq view = %q state=%q, want 1/ready", v.Items[0].Detail, v.State)
	}
	mu.Lock()
	defer mu.Unlock()
	if plays != 0 {
		t.Errorf("sound/play called %d times on an unsupported host, want 0", plays)
	}
	if d.sound != soundOff {
		t.Errorf("sound probe = %d, want latched off", d.sound)
	}
}

// TestBarkAfterSupportedProbe walks the full probe path — whoami grants
// sound, status reports supported — and the equals press barks.
func TestBarkAfterSupportedProbe(t *testing.T) {
	d := &Dog{pad: calculator.NewPad()}
	_, r := launchDogInstance(t, d)

	if err := r.Handle("session/whoami", func(context.Context, ipc.Request) (any, error) {
		return map[string]any{"capabilities": []string{"ipc", "sound"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(soundStatusRoute, func(context.Context, ipc.Request) (any, error) {
		return map[string]any{"supported": true, "player": "fake"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	plays := 0
	if err := r.Handle(soundRoute, func(context.Context, ipc.Request) (any, error) {
		mu.Lock()
		plays++
		mu.Unlock()
		return map[string]bool{"played": true}, nil
	}); err != nil {
		t.Fatal(err)
	}

	v := fetchView(t, r, 2)
	press := cellPress(t, r, v.Instance, 2)
	press("digit_1")
	press("eq")
	mu.Lock()
	defer mu.Unlock()
	if plays != 1 {
		t.Errorf("sound/play called %d times, want 1", plays)
	}
	if d.sound != soundOn {
		t.Errorf("sound probe = %d, want latched on", d.sound)
	}
}

func TestRelaunchReset(t *testing.T) {
	bus := events.NewBus()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := ipc.NewRouter()
	reg := app.NewRegistry()
	if err := reg.RegisterBuiltin(Manifest(), Factory); err != nil {
		t.Fatal(err)
	}
	mgr := app.NewManager(reg, process.NewManager(bus, log), router, bus, log)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p, err := mgr.Launch(ctx, ID)
	if err != nil {
		t.Fatal(err)
	}

	v := fetchView(t, router, 2)
	press := cellPress(t, router, v.Instance, 2)
	press("digit_5")

	if err := mgr.Stop(ID, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("first launch did not exit")
	}

	// Relaunch: the instance must start fresh.
	p2, err := mgr.Launch(ctx, ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = mgr.Stop(ID, 2*time.Second)
		<-p2.Done()
	}()

	resp := dispatch(t, router, 3, "view", `{"version":2}`)
	if !resp.OK {
		t.Fatalf("view after relaunch failed: %s", resp.Error)
	}
	v = mustView(t, resp.Data)
	if v.Items[0].Detail != "0" {
		t.Errorf("after relaunch display = %q, want 0", v.Items[0].Detail)
	}
}
