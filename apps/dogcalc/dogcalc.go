// Package dogcalc is the pack's calculator with dogs for buttons: the same
// arithmetic core as the retired version-1 Calculator, rendered as a
// contract-v2 grid pad of named-breed digit keys and dog-verb operator keys.
// Version-1 callers get the same sixteen keys as labeled actions.
package dogcalc

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"gostalgia/apps/calculator"
	"gostalgia/sdk"
)

// ID is the reverse-DNS identifier for Dogcalc.
const ID = "com.gostalgia.dogcalc"

// padColumns lays the pad out four keys wide like a desktop calculator.
const padColumns = 4

// pressAction is the shared action every grid cell declares; cell_id carries
// which key was booped.
const pressAction = "press"

// soundCapability and soundRoute name the optional audio grant and route from
// the host audio adapter work. The manifest cannot declare a grant the SDK
// does not yet define, so the bark stays wired but dormant: it plays only if
// this instance's grant ever reports the capability and the route exists.
const (
	soundCapability = "sound"
	soundRoute      = "sound/play"
)

// Tri-state for the sound probe: unknown until checked once, then latched.
const (
	soundUnknown = iota
	soundOff
	soundOn
)

// key labels pair the arithmetic symbol with its dog, so the math still reads
// plainly. Digit keys name breeds; operator and utility keys are dog verbs.
var keyLabels = map[string]string{
	"digit_0": "0 Basset",
	"digit_1": "1 Corgi",
	"digit_2": "2 Beagle",
	"digit_3": "3 Pug",
	"digit_4": "4 Husky",
	"digit_5": "5 Dachshund",
	"digit_6": "6 Labrador",
	"digit_7": "7 Poodle",
	"digit_8": "8 Boxer",
	"digit_9": "9 Shiba",
	"add":     "FETCH +",
	"sub":     "BURY -",
	"mul":     "LITTER x",
	"div":     "SHARE /",
	"eq":      "BOOP =",
	"clear":   "WAG C",
	"dot":     "PAW .",
	"neg":     "ROLL ±",
}

// padOrder is the row-major key order of the grid pad, four columns wide:
// utility keys up top, digits in calculator order, boop at the right edge,
// and a short final row of zero and dot.
var padOrder = []string{
	"clear", "neg", "div", "mul",
	"digit_7", "digit_8", "digit_9", "sub",
	"digit_4", "digit_5", "digit_6", "add",
	"digit_1", "digit_2", "digit_3", "eq",
	"digit_0", "dot",
}

// v1Order lists keys for the contract-v1 action fallback, bounded by the
// 16-action limit — exactly the v1 Calculator's key set with dog labels.
var v1Order = []string{
	"digit_0", "digit_1", "digit_2", "digit_3", "digit_4",
	"digit_5", "digit_6", "digit_7", "digit_8", "digit_9",
	"add", "sub", "mul", "div", "eq", "clear",
}

//go:embed manifest.json
var manifestJSON []byte

// Manifest returns the validated manifest declaration.
func Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestJSON)
	if err != nil {
		panic(err) // invalid compiled-in data is a programming error
	}
	return m
}

// ManifestJSON returns a copy of the raw embedded JSON.
func ManifestJSON() []byte { return append([]byte(nil), manifestJSON...) }

// Dog is the running in-process instance of Dogcalc.
type Dog struct {
	app   *sdk.Context
	mu    sync.Mutex
	pad   *calculator.Pad
	sound int
}

// Factory returns a fresh, uninitialized Dog instance.
func Factory() (sdk.Instance, error) {
	return &Dog{pad: calculator.NewPad()}, nil
}

// Init registers the presentation surface and the headless calc route.
func (d *Dog) Init(app *sdk.Context) error {
	d.app = app
	if err := app.Present(d.view, d.act); err != nil {
		return err
	}
	return app.Handle("calc", d.calc)
}

// Run blocks until the application is cancelled.
func (d *Dog) Run(ctx context.Context) error {
	d.app.Log.Info("dogcalc running")
	<-ctx.Done()
	return nil
}

// Stop is called once on exit, including after a failed Init.
func (d *Dog) Stop(context.Context) error { return nil }

func (d *Dog) view(ctx context.Context) (sdk.View, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.viewLocked(sdk.PresentationRequestVersion(ctx)), nil
}

func (d *Dog) viewLocked(version int) sdk.View {
	v := sdk.View{
		Title:  "Dogcalc",
		State:  sdk.ViewReady,
		Items:  []sdk.Item{{ID: "display", Label: "Display", Detail: d.pad.Display()}},
		Status: "Every button is a good dog",
	}
	if version >= 2 {
		v.Grid = d.gridLocked()
	} else {
		v.Actions = v1Actions()
	}
	if err := d.pad.Err(); err != "" {
		v.State = sdk.ViewError
		v.Error = err
		v.Status = err
	}
	return v
}

// gridLocked builds the pad snapshot, dimming the dot key once the operand
// being edited already has its decimal point.
func (d *Dog) gridLocked() *sdk.Grid {
	cells := make([]sdk.Cell, 0, len(padOrder))
	for _, id := range padOrder {
		cells = append(cells, sdk.Cell{
			ID:       id,
			Label:    keyLabels[id],
			Action:   pressAction,
			Disabled: id == "dot" && d.pad.HasDot(),
		})
	}
	return &sdk.Grid{Label: "Paw pad", Columns: padColumns, Cells: cells}
}

func v1Actions() []sdk.Action {
	actions := make([]sdk.Action, 0, len(v1Order))
	for _, id := range v1Order {
		actions = append(actions, sdk.Action{ID: id, Label: keyLabels[id]})
	}
	return actions
}

func (d *Dog) act(ctx context.Context, p sdk.ActionRequest) (sdk.View, error) {
	key := p.Action
	if p.CellID != "" {
		// A grid press names its key in cell_id.
		key = p.CellID
	}
	d.mu.Lock()
	if !d.pressLocked(key) {
		d.mu.Unlock()
		return sdk.View{}, fmt.Errorf("unknown action %q", p.Action)
	}
	v := d.viewLocked(sdk.PresentationRequestVersion(ctx))
	d.mu.Unlock()
	if key == "eq" {
		d.bark(ctx)
	}
	return v, nil
}

// pressLocked applies one pad key by its action/cell id.
func (d *Dog) pressLocked(key string) bool {
	switch {
	case strings.HasPrefix(key, "digit_"):
		n, err := strconv.Atoi(strings.TrimPrefix(key, "digit_"))
		if err != nil || n < 0 || n > 9 {
			return false
		}
		d.pad.Digit(n)
	case key == "add":
		d.pad.Op("+")
	case key == "sub":
		d.pad.Op("-")
	case key == "mul":
		d.pad.Op("*")
	case key == "div":
		d.pad.Op("/")
	case key == "eq":
		d.pad.Eq()
	case key == "clear":
		d.pad.Clear()
	case key == "dot":
		d.pad.Dot()
	case key == "neg":
		d.pad.Neg()
	default:
		return false
	}
	return true
}

// bark plays the equals-key bark when this instance's grant reports the sound
// capability and a host audio adapter answers soundRoute. It probes the grant
// once per launch and swallows every failure: no grant, no route, or a dead
// adapter must never turn into a calculator error.
func (d *Dog) bark(ctx context.Context) {
	d.mu.Lock()
	sound := d.sound
	d.mu.Unlock()
	if sound == soundUnknown {
		sound = soundOff
		var who struct {
			Capabilities []string `json:"capabilities"`
		}
		if err := d.app.Call(ctx, "session/whoami", nil, &who); err == nil {
			for _, cap := range who.Capabilities {
				if cap == soundCapability {
					sound = soundOn
				}
			}
		}
		d.mu.Lock()
		d.sound = sound
		d.mu.Unlock()
	}
	if sound != soundOn {
		return
	}
	_ = d.app.Call(ctx, soundRoute, map[string]string{"sound": "bark"}, nil)
}

// calc is the headless route that evaluates a full expression string, the
// same contract the version-1 Calculator's calc route serves.
func (d *Dog) calc(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Expr string `json:"expr"`
	}
	if err := sdk.DecodeParams(raw, &p); err != nil {
		return nil, err
	}
	if p.Expr == "" {
		return nil, fmt.Errorf("expr is required")
	}
	res, err := calculator.Evaluate(p.Expr)
	if err != nil {
		return nil, err
	}
	return map[string]string{"result": res}, nil
}
