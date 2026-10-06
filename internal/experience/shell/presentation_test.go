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
