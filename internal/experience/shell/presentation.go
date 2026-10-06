package shell

import (
	"context"
	"crypto/rand"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gostalgia/internal/experience/theme"
	"gostalgia/internal/experience/ui"
	"gostalgia/sdk"
)

// appView is shell-owned state. No app supplies a tea.Model, keymap, style, or
// terminal handle. Epoch/sequence guards reject replies after dismissal,
// cancellation, or a later operation.
type appView struct {
	id, instance string
	pid          int32
	epoch, seq   uint64
	data         sdk.View
	values       map[string]string
	focus, item  int
	itemFocus    bool
	busy         bool
	request      string
	cancel       context.CancelFunc
	banner       string
}

type viewMsg struct {
	epoch, seq uint64
	data       sdk.View
	err        error
	action     bool
}

type viewTickMsg struct{ epoch uint64 }
type viewCheckMsg struct {
	epoch, seq uint64
	apps       []appStatus
	data       *sdk.View
	err        error
	viewErr    error
}
type viewCancelMsg struct{ err error }

func (m *Model) openView(a appStatus) tea.Cmd {
	m.viewEpoch++
	v := &appView{
		id: a.Manifest.ID, pid: a.PID, epoch: m.viewEpoch,
		data:   sdk.View{Title: a.Manifest.Name, State: sdk.ViewLoading},
		values: make(map[string]string),
	}
	m.presentation, m.shelf, m.scroll = v, false, 0
	epoch, ctx, client, id := v.epoch, m.ctx, m.client, v.id
	return tea.Batch(func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var data sdk.View
		err := client.Call(ctx, "app/"+id+"/view", sdk.ViewRequest{Version: sdk.PresentationVersion}, &data)
		if err == nil {
			err = data.Validate()
		}
		return viewMsg{epoch: epoch, data: data, err: err}
	}, viewTick(epoch))
}

func viewTick(epoch uint64) tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return viewTickMsg{epoch} })
}

// dismissView retracts focus and data immediately. Cancel uses a separate IPC
// call because canceling a socket client's wait alone cannot stop server work.
func (m *Model) dismissView() tea.Cmd {
	v := m.presentation
	m.presentation = nil
	m.viewEpoch++
	if v == nil || v.cancel == nil {
		return nil
	}
	return m.cancelViewAction(v)
}

func (m *Model) cancelViewAction(v *appView) tea.Cmd {
	v.cancel()
	v.cancel = nil
	v.busy = false
	v.seq++
	v.banner = "Canceled. Work already committed cannot be undone."
	params := sdk.CancelRequest{Version: sdk.PresentationVersion, Instance: v.instance, RequestID: v.request}
	ctx, client, id := context.WithoutCancel(m.ctx), m.client, v.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		err := client.Call(ctx, "app/"+id+"/cancel", params, nil)
		return viewCancelMsg{err}
	}
}

func (m *Model) checkView() tea.Cmd {
	v := m.presentation
	epoch, seq, id, pid, busy := v.epoch, v.seq, v.id, v.pid, v.busy
	ctx, client := m.ctx, m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		msg := viewCheckMsg{epoch: epoch, seq: seq}
		msg.err = client.Call(ctx, "app/list", nil, &msg.apps)
		if msg.err == nil && !busy {
			for _, a := range msg.apps {
				if a.Manifest.ID == id && a.Running && a.PID == pid {
					var data sdk.View
					msg.viewErr = client.Call(ctx, "app/"+id+"/view", sdk.ViewRequest{Version: sdk.PresentationVersion}, &data)
					if msg.viewErr == nil {
						msg.viewErr = data.Validate()
						if msg.viewErr == nil {
							msg.data = &data
						}
					}
					break
				}
			}
		}
		return msg
	}
}

func (m *Model) applyView(data sdk.View) bool {
	v := m.presentation
	if v.instance != "" && v.instance != data.Instance {
		return false
	}
	var itemID, fieldID, actionID string
	if v.item < len(v.data.Items) {
		itemID = v.data.Items[v.item].ID
	}
	if v.focus < len(v.data.Fields) {
		fieldID = v.data.Fields[v.focus].ID
	} else if index := v.focus - len(v.data.Fields); index < len(v.data.Actions) {
		actionID = v.data.Actions[index].ID
	}
	v.instance, v.data = data.Instance, data
	v.focus, v.item = 0, 0
	fields := make(map[string]string)
	for i, f := range data.Fields {
		value, exists := v.values[f.ID]
		if !exists {
			value = f.Value
		}
		fields[f.ID] = value
		if f.ID == fieldID {
			v.focus = i
		}
	}
	for i, a := range data.Actions {
		if a.ID == actionID {
			v.focus = len(data.Fields) + i
		}
	}
	for i, item := range data.Items {
		if item.ID == itemID {
			v.item = i
		}
	}
	v.values = fields
	return true
}

func (m *Model) updatePresentation(msg tea.Msg) tea.Cmd {
	v := m.presentation
	switch msg := msg.(type) {
	case viewMsg:
		if v == nil || msg.epoch != v.epoch || msg.seq != v.seq {
			return nil
		}
		if msg.action {
			v.busy = false
			if v.cancel != nil {
				v.cancel()
				v.cancel = nil
			}
		}
		if msg.err != nil {
			v.banner = safe(msg.err.Error())
			if v.instance == "" {
				v.data.State = sdk.ViewError
			}
		} else if !m.applyView(msg.data) {
			m.append(entry{"App instance changed; view closed.", "error"})
			return m.dismissView()
		} else {
			v.banner = ""
		}
	case viewTickMsg:
		if v != nil && msg.epoch == v.epoch {
			return m.checkView()
		}
	case viewCheckMsg:
		if v == nil || msg.epoch != v.epoch {
			return nil
		}
		alive := false
		for _, a := range msg.apps {
			alive = alive || a.Manifest.ID == v.id && a.Running && a.PID == v.pid
		}
		if msg.err != nil || !alive {
			text := "App exited or stopped; view closed."
			if msg.err != nil {
				text = "App view unavailable: " + msg.err.Error()
			}
			m.append(entry{text, "error"})
			return m.dismissView()
		}
		m.apps = msg.apps
		if msg.seq == v.seq && msg.viewErr != nil {
			v.banner = safe(msg.viewErr.Error())
		}
		if msg.seq == v.seq && msg.data != nil && !m.applyView(*msg.data) {
			m.append(entry{"App instance changed; view closed.", "error"})
			return m.dismissView()
		}
		return viewTick(v.epoch)
	case viewCancelMsg:
		if msg.err != nil {
			m.append(entry{"Cancel request: " + msg.err.Error(), "error"})
		}
	}
	return nil
}

func (m *Model) viewKey(key tea.KeyMsg) tea.Cmd {
	v := m.presentation
	if key.String() == "esc" {
		if v.busy {
			return m.cancelViewAction(v)
		}
		return m.dismissView()
	}
	if v.busy || v.instance == "" {
		return nil
	}
	fields, actions := v.data.Fields, v.data.Actions
	total := len(fields) + len(actions)
	switch key.String() {
	case "tab", "shift+tab":
		v.itemFocus, m.scroll = false, 0
		if total > 0 {
			delta := 1
			if key.String() == "shift+tab" {
				delta = total - 1
			}
			v.focus = (v.focus + delta) % total
		}
	case "up":
		v.itemFocus, m.scroll = true, 0
		v.item = max(0, v.item-1)
	case "down":
		v.itemFocus, m.scroll = true, 0
		v.item = min(max(0, len(v.data.Items)-1), v.item+1)
	case "enter":
		index := v.focus - len(fields)
		if index < 0 {
			index = 0
		}
		if index >= len(actions) || actions[index].Disabled || v.data.State == sdk.ViewLoading {
			return nil
		}
		values := make(map[string]string)
		for id, value := range v.values {
			values[id] = value
		}
		p := sdk.ActionRequest{
			Version: sdk.PresentationVersion, Instance: v.instance,
			RequestID: "r" + strings.ToLower(rand.Text()), Action: actions[index].ID, Values: values,
		}
		if len(v.data.Items) > 0 {
			p.ItemID = v.data.Items[v.item].ID
		}
		v.seq++
		v.request, v.busy, v.banner = p.RequestID, true, ""
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		v.cancel = cancel
		epoch, seq, id, client := v.epoch, v.seq, v.id, m.client
		return func() tea.Msg {
			defer cancel()
			var data sdk.View
			err := client.Call(ctx, "app/"+id+"/action", p, &data)
			if err == nil {
				err = data.Validate()
			} else {
				// A timeout/disconnect of the local wait is not server-side
				// cancellation. Best-effort cancel using a fresh deadline.
				cancelCtx, stop := context.WithTimeout(context.WithoutCancel(m.ctx), 2*time.Second)
				_ = client.Call(cancelCtx, "app/"+id+"/cancel", sdk.CancelRequest{
					Version: p.Version, Instance: p.Instance, RequestID: p.RequestID,
				}, nil)
				stop()
			}
			return viewMsg{epoch: epoch, seq: seq, data: data, err: err, action: true}
		}
	default:
		if v.focus >= len(fields) {
			return nil
		}
		id := fields[v.focus].ID
		v.itemFocus, m.scroll = false, 0
		value := []rune(v.values[id])
		switch key.Type {
		case tea.KeyBackspace, tea.KeyCtrlH:
			if len(value) > 0 {
				value = value[:len(value)-1]
			}
		case tea.KeyCtrlU:
			value = nil
		case tea.KeyRunes, tea.KeySpace:
			text := strings.ReplaceAll(safe(string(key.Runes)), "\n", " ")
			if key.Type == tea.KeySpace {
				text = " "
			}
			if len(string(value))+len(text) <= 4096 {
				value = append(value, []rune(text)...)
			}
		}
		v.values[id] = string(value)
	}
	return nil
}

func viewText(s string) string { return strings.ReplaceAll(safe(s), "\n", " ") }

func (m *Model) viewLines(height int) []string {
	v := m.presentation
	lines := []string{m.kit.Heading(viewText(v.data.Title)), m.kit.Muted("Tab focus · Enter action · ↑↓ item · Esc cancel/back")}
	if v.busy || v.data.State == sdk.ViewLoading {
		lines = append(lines, m.kit.Badge("Loading…", theme.Busy, max(0, m.width-4)))
	}
	if v.banner != "" {
		lines = append(lines, m.kit.StatusText(viewText(v.banner), theme.Error))
	}
	if v.data.Error != "" {
		lines = append(lines, m.kit.StatusText(viewText(v.data.Error), theme.Error))
	}
	var rows []string
	for i, item := range v.data.Items {
		text := viewText(item.Label + ": " + item.Detail)
		if i == v.item {
			text = m.kit.FocusText(text)
		} else {
			text = m.kit.Text(text)
		}
		rows = append(rows, text)
	}
	for i, field := range v.data.Fields {
		label := viewText(field.Label) + ": "
		value := viewText(v.values[field.ID])
		room := max(1, m.width-8-lipgloss.Width(label))
		text := label + ui.Tail(value, room)
		if i == v.focus {
			text = m.kit.FocusText(text + " ")
		} else {
			text = m.kit.Text(text)
		}
		rows = append(rows, text)
	}
	for i, action := range v.data.Actions {
		text := "[" + viewText(action.Label) + "]"
		if action.Disabled || v.busy || v.data.State == sdk.ViewLoading {
			text += " (disabled)"
			text = m.kit.StatusText(text, theme.Disabled)
		} else if len(v.data.Fields)+i == v.focus {
			text = m.kit.FocusText(text)
		} else {
			text = m.kit.Text(text)
		}
		rows = append(rows, text)
	}
	visible := max(1, height-len(lines)-1)
	focus := len(v.data.Items) + v.focus
	if v.itemFocus {
		focus = v.item
	}
	start := max(0, min(focus-visible+1, max(0, len(rows)-visible))-m.scroll)
	lines = append(lines, rows[start:min(len(rows), start+visible)]...)
	lines = append(lines, m.kit.Muted(viewText(v.data.Status)))
	return lines
}

func (m *Model) selectedView() tea.Cmd {
	if len(m.apps) == 0 || !m.apps[m.selected].Running {
		m.append(entry{"Launch an app before opening its view.", "error"})
		return nil
	}
	if m.busy {
		return nil
	}
	return m.openView(m.apps[m.selected])
}
