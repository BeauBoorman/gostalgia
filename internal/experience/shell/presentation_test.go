package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"gostalgia/internal/experience/theme"
	"gostalgia/internal/experience/ui"
	"gostalgia/sdk"
)

type callerFunc func(context.Context, string, any, any) error

func (f callerFunc) Call(ctx context.Context, method string, params, out any) error {
	return f(ctx, method, params, out)
}

func screenData() sdk.View {
	return sdk.View{
		Version: 1, Instance: "launch", Title: "Screen", State: sdk.ViewReady,
		Items:   []sdk.Item{{ID: "one", Label: "One", Detail: "first"}, {ID: "two", Label: "Two", Detail: "second"}},
		Fields:  []sdk.Field{{ID: "text", Label: "Text", Required: true}},
		Actions: []sdk.Action{{ID: "send", Label: "Send"}},
	}
}

// padData is a version-2 snapshot exercising every new element: a multi-line
// art block, meters (one out of range for the clamp), and a grid pad with a
// cell-declared action, a disabled cell, and a datum cell.
func padData() sdk.View {
	return sdk.View{
		Version: 2, Instance: "launch", Title: "Pad", State: sdk.ViewReady,
		Items:   []sdk.Item{{ID: "one", Label: "One", Detail: "first"}},
		Fields:  []sdk.Field{{ID: "text", Label: "Text"}},
		Actions: []sdk.Action{{ID: "adopt", Label: "Adopt"}},
		Blocks:  []sdk.Block{{ID: "art", Label: "Mascot", Text: "(\\_/)\n(o.o)\n> ^ <"}},
		Meters: []sdk.Meter{
			{ID: "charge", Label: "Charge", Value: 0.5},
			{ID: "hunger", Label: "Hunger", Value: 1.4},
		},
		Grid: &sdk.Grid{Label: "Pad", Columns: 2, Cells: []sdk.Cell{
			{ID: "nw", Label: "NW", Action: "press"},
			{ID: "ne", Label: "NE", Action: "press"},
			{ID: "sw", Label: "SW"},
			{ID: "se", Label: "SE", Disabled: true},
		}},
		Status: "waiting",
	}
}

func screenStatus() appStatus {
	var a appStatus
	a.Manifest.ID, a.Manifest.Name, a.Running, a.PID = "com.test.screen", "Screen", true, 42
	return a
}

func writeJSON(out, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func TestPresentationLoadingFocusAndSemanticActions(t *testing.T) {
	var received sdk.ActionRequest
	m := New(context.Background(), callerFunc(func(ctx context.Context, method string, params, out any) error {
		v := screenData()
		if strings.HasSuffix(method, "/action") {
			received = params.(sdk.ActionRequest)
			v.Status = "Sent " + received.Values["text"]
		}
		return writeJSON(out, v)
	}), nil)
	a := screenStatus()
	m.apps = []appStatus{a}
	_, open := m.Update(tea.KeyMsg{Type: tea.KeyF4})
	if m.presentation == nil || !strings.Contains(m.View(), "Loading") {
		t.Fatal("opening has no loading state")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ignored while loading")})
	batch := open().(tea.BatchMsg)
	m.Update(batch[0]())
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("héllo")})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	_, action := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if action == nil || !m.presentation.busy || len(m.input) != 0 {
		t.Fatal("app did not own focus while shell retained terminal ownership")
	}
	m.Update(action())
	if received.Action != "send" || received.Values["text"] != "héllo" || received.ItemID != "two" ||
		received.Instance != "launch" || received.Version != 1 || received.RequestID == "" {
		t.Fatalf("semantic action routing: %+v", received)
	}
	if m.presentation.busy || !strings.Contains(m.View(), "Sent héllo") {
		t.Fatal("action snapshot not rendered")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.presentation != nil {
		t.Fatal("Esc did not return focus to the shell")
	}
}

func TestPresentationCancellationAndLateReplies(t *testing.T) {
	var canceled sdk.CancelRequest
	m := New(context.Background(), callerFunc(func(ctx context.Context, method string, params, out any) error {
		if strings.HasSuffix(method, "/cancel") {
			canceled = params.(sdk.CancelRequest)
			return nil
		}
		return writeJSON(out, screenData())
	}), nil)
	m.openView(screenStatus())
	epoch := m.presentation.epoch
	m.Update(viewMsg{epoch: epoch, data: screenData()})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	_, action := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if action == nil {
		t.Fatal("action not dispatched")
	}
	oldSeq, request := m.presentation.seq, m.presentation.request
	_, cancel := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cancel == nil {
		t.Fatal("no cancellation route")
	}
	m.Update(cancel())
	if canceled.RequestID != request || canceled.Instance != "launch" || m.presentation.busy {
		t.Fatal("Esc did not cancel the owning operation")
	}
	late := screenData()
	late.Status = "late reply must not render"
	m.Update(viewMsg{epoch: epoch, seq: oldSeq, data: late, action: true})
	if strings.Contains(m.View(), late.Status) {
		t.Fatal("canceled operation replaced current view")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.openView(screenStatus())
	m.Update(viewMsg{epoch: epoch, data: late})
	if m.presentation.data.Status == late.Status {
		t.Fatal("dismissed view's reply reopened it")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if m.presentation != nil || !m.shelf {
		t.Fatal("global shelf hotkey lost ownership")
	}
}

func TestPresentationLifecycleRetraction(t *testing.T) {
	for _, cause := range []string{"exit", "relaunch", "transport", "instance"} {
		t.Run(cause, func(t *testing.T) {
			m := New(context.Background(), noopCaller{}, nil)
			a := screenStatus()
			m.openView(a)
			epoch := m.presentation.epoch
			m.Update(viewMsg{epoch: epoch, data: screenData()})
			msg := viewCheckMsg{epoch: epoch, apps: []appStatus{a}}
			switch cause {
			case "exit":
				msg.apps[0].Running = false
			case "relaunch":
				msg.apps[0].PID++
			case "transport":
				msg.err = errors.New("connection closed")
			case "instance":
				data := screenData()
				data.Instance = "new-launch"
				msg.data = &data
			}
			m.Update(msg)
			if m.presentation != nil || !strings.Contains(m.View(), "view") {
				t.Fatal("lost app still owns focus or view")
			}
			m.Update(viewMsg{epoch: epoch, data: screenData()})
			if m.presentation != nil {
				t.Fatal("late lifecycle reply restored retracted view")
			}
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("prompt works")})
			if string(m.input) != "prompt works" {
				t.Fatal("shell focus was not restored")
			}
		})
	}
}

func TestPresentationErrorsBoundsAndTerminalControls(t *testing.T) {
	m := New(context.Background(), noopCaller{}, nil)
	m.openView(screenStatus())
	epoch := m.presentation.epoch
	m.Update(viewMsg{epoch: epoch, err: errors.New("view unavailable")})
	if !strings.Contains(m.View(), "view unavailable") {
		t.Fatal("load error banner missing")
	}
	data := screenData()
	data.Title += "\x1b]52;c;clipboard\a"
	data.Error = "failure\r\x1b[31m"
	data.State = sdk.ViewError
	data.Items[0].Detail = strings.Repeat("界", 5000)
	m.Update(viewMsg{epoch: epoch, data: data})
	for _, size := range [][2]int{{80, 24}, {30, 10}, {20, 8}, {120, 40}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := m.View()
		if strings.Contains(view, "\a") || strings.Contains(view, "\r") || strings.Contains(view, "clipboard") {
			t.Fatal("app-controlled terminal sequences were rendered")
		}
		if lipgloss.Height(view) > size[1] || lipgloss.Width(view) > size[0] {
			t.Fatal("app view escaped terminal bounds")
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.presentation.data.Title != data.Title {
		t.Fatal("global quit was routed to an app")
	}
}

func TestPresentationRefreshPreservesSemanticFocusAndVisibleSelection(t *testing.T) {
	m := New(context.Background(), noopCaller{}, nil)
	m.openView(screenStatus())
	m.applyView(screenData())
	v := m.presentation
	v.values["text"], v.focus, v.item = "draft", 1, 1
	data := screenData()
	data.Fields = append([]sdk.Field{{ID: "new", Label: "New"}}, data.Fields...)
	data.Items[0], data.Items[1] = data.Items[1], data.Items[0]
	m.applyView(data)
	if v.values["text"] != "draft" || v.focus != 2 || v.item != 0 || v.data.Items[v.item].ID != "two" {
		t.Fatal("refresh moved semantic focus/selection or discarded a draft")
	}
	data.Items = nil
	for i := 0; i < 100; i++ {
		data.Items = append(data.Items, sdk.Item{ID: fmt.Sprintf("item-%d", i), Label: fmt.Sprintf("ROW-%03d", i)})
	}
	m.applyView(data)
	m.height = 16
	if !strings.Contains(m.View(), "[Send]") {
		t.Fatal("focused action hidden below a long list")
	}
	v.item, v.itemFocus = 99, true
	if !strings.Contains(m.View(), "ROW-099") {
		t.Fatal("selected item hidden below a long list")
	}
	m.Update(viewCheckMsg{epoch: v.epoch, apps: []appStatus{screenStatus()}, viewErr: errors.New("temporary snapshot failure")})
	if m.presentation == nil || !strings.Contains(m.View(), "temporary snapshot failure") {
		t.Fatal("recoverable snapshot failure did not keep an error banner")
	}
}

func TestPresentationUsesExplicitThemeAndFocusTokens(t *testing.T) {
	tokens := theme.Nostalgia()
	tokens.Focus.Marker = "*"
	custom := NewWithTheme(context.Background(), noopCaller{}, nil, tokens, ui.Plain)
	data := screenData()
	data.Actions[0].Disabled = true
	custom.presentation = &appView{
		data: data, instance: data.Instance, focus: 1,
		values: map[string]string{"text": "e\u0301👩‍💻界"},
	}
	view := custom.View()
	if strings.Contains(view, "\x1b") || !strings.Contains(view, "* One") ||
		!strings.Contains(view, "[Send] (disabled)") || strings.Contains(view, "* [Send]") ||
		!strings.Contains(view, "(Enter) ACTION") {
		t.Fatalf("app view ignored plain mode, focus marker, or disabled state:\n%s", view)
	}
	var rendered []string
	for _, tokens := range []theme.Theme{theme.Nostalgia(), theme.Midnight()} {
		m := NewWithTheme(context.Background(), noopCaller{}, nil, tokens, ui.TrueColor)
		m.presentation = &appView{
			data: data, instance: data.Instance, values: map[string]string{"text": "e\u0301👩‍💻界"},
		}
		rendered = append(rendered, m.View())
		for _, size := range []tea.WindowSizeMsg{
			{Width: 30, Height: 10}, {Width: 80, Height: 24}, {Width: 120, Height: 40}, {Width: 240, Height: 80},
		} {
			m.Update(size)
			if view := m.View(); lipgloss.Width(view) != size.Width || lipgloss.Height(view) != size.Height {
				t.Fatalf("app view escaped %dx%d bounds", size.Width, size.Height)
			}
		}
	}
	if rendered[0] == rendered[1] || ansi.Strip(rendered[0]) != ansi.Strip(rendered[1]) {
		t.Fatal("app palettes must differ without changing layout")
	}
}

func TestPresentationGridNavigationAndCellActions(t *testing.T) {
	var received sdk.ActionRequest
	m := New(context.Background(), callerFunc(func(ctx context.Context, method string, params, out any) error {
		v := padData()
		if strings.HasSuffix(method, "/action") {
			received = params.(sdk.ActionRequest)
			v.Status = "pressed " + received.CellID
		}
		return writeJSON(out, v)
	}), nil)
	m.openView(screenStatus())
	epoch := m.presentation.epoch
	m.Update(viewMsg{epoch: epoch, data: padData()})
	v := m.presentation
	if v.version != 2 {
		t.Fatalf("negotiated version = %d, want 2", v.version)
	}
	// The grid is the last tab stop: field, action, then grid.
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !v.gridFocused() {
		t.Fatal("tab ring did not reach the grid stop")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if v.cell != 1 {
		t.Fatalf("cell = %d, want 1", v.cell)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("cell enter did not dispatch its action")
	}
	m.Update(cmd())
	if received.Action != "press" || received.CellID != "ne" || received.Version != 2 ||
		received.Instance != "launch" || received.RequestID == "" {
		t.Fatalf("cell action routing: %+v", received)
	}
	// Down lands on the disabled SE cell; activation is a no-op.
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("disabled cell dispatched an action")
	}
	// Left lands on the datum cell (no action); activation is a no-op.
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("datum cell dispatched an action")
	}
	// Up clamps back to NW rather than wrapping.
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if v.cell != 0 {
		t.Fatalf("cell = %d after clamping, want 0", v.cell)
	}
	// A list action rides along with the selected cell_id and item_id.
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if v.gridFocused() {
		t.Fatal("shift+tab stayed on the grid")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("focused action did not dispatch")
	}
	m.Update(cmd())
	if received.Action != "adopt" || received.CellID != "nw" || received.ItemID != "one" {
		t.Fatalf("list action lost selection: %+v", received)
	}
	// Refresh preserves grid focus and selection by ID.
	v.focus = len(v.data.Fields) + len(v.data.Actions)
	v.cell = 3
	data := padData()
	data.Grid.Cells[0], data.Grid.Cells[3] = data.Grid.Cells[3], data.Grid.Cells[0]
	m.applyView(data)
	if !v.gridFocused() || v.data.Grid.Cells[v.cell].ID != "se" {
		t.Fatalf("refresh lost grid focus/selection: focus=%d cell=%d", v.focus, v.cell)
	}
}

func TestPresentationVersionNegotiationFallback(t *testing.T) {
	var requested []int
	m := New(context.Background(), callerFunc(func(ctx context.Context, method string, params, out any) error {
		if strings.HasSuffix(method, "/view") {
			p := params.(sdk.ViewRequest)
			requested = append(requested, p.Version)
			if p.Version > 1 {
				return errors.New("presentation: unsupported version 2")
			}
		}
		return writeJSON(out, screenData())
	}), nil)
	batch := m.openView(screenStatus())().(tea.BatchMsg)
	m.Update(batch[0]())
	if v := m.presentation; v == nil || v.version != 1 {
		t.Fatalf("negotiated version = %+v", m.presentation)
	}
	if len(requested) != 2 || requested[0] != 2 || requested[1] != 1 {
		t.Fatalf("negotiation requests = %v, want [2 1]", requested)
	}
	// A version-2 app answers at 2 without a fallback request.
	m2 := New(context.Background(), callerFunc(func(ctx context.Context, method string, params, out any) error {
		return writeJSON(out, padData())
	}), nil)
	batch = m2.openView(screenStatus())().(tea.BatchMsg)
	m2.Update(batch[0]())
	if v := m2.presentation; v == nil || v.version != 2 || v.data.Grid == nil {
		t.Fatalf("version-2 negotiation: %+v", m2.presentation)
	}
}

func TestPresentationV2ElementsStayInBounds(t *testing.T) {
	m := New(context.Background(), noopCaller{}, nil)
	m.openView(screenStatus())
	epoch := m.presentation.epoch
	data := padData()
	data.Blocks[0].Text = strings.Repeat("界"+strings.Repeat("=", 200)+"\n", 32)
	data.Grid.Label += "\x1b[31m\r"
	m.Update(viewMsg{epoch: epoch, data: data})
	for _, size := range [][2]int{{80, 24}, {30, 10}, {120, 40}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := m.View()
		if strings.Contains(view, "\a") || strings.Contains(view, "\r") {
			t.Fatal("version-2 elements leaked terminal controls")
		}
		if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
			t.Fatalf("version-2 view escaped %dx%d bounds", size[0], size[1])
		}
	}
}
