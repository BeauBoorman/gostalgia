// Package shell is Gostalgia's Charm experience layer. Runtime and SDK never
// import it. All environment operations use Caller, not privileged internals.
package shell

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gostalgia/internal/experience/theme"
	"gostalgia/internal/experience/ui"
	"gostalgia/sdk"
)

const maxTranscript = 400

type entry struct {
	text string
	kind string
}
type closedMsg struct{}

type viewMode int

const (
	modeHome viewMode = iota
	modeLauncher
	modePrompt
)

type FocusState int

const (
	FocusPrompt FocusState = iota
	FocusHome
	FocusLauncher
	FocusApp
	FocusPalette
)

func (f FocusState) String() string {
	switch f {
	case FocusHome:
		return "home"
	case FocusLauncher:
		return "launcher"
	case FocusApp:
		return "app"
	case FocusPalette:
		return "palette"
	default:
		return "prompt"
	}
}

// Model owns UI state; tea.Cmd does IO off the event-loop goroutine.
type Model struct {
	ctx           context.Context
	client        Caller
	closed        <-chan struct{}
	width, height int
	cwd           string
	input         []rune
	cursor        int
	transcript    []entry
	history       []string
	historyPos    int
	draft         string
	apps          []appStatus
	selected      int
	shelf         bool
	busy          bool
	scroll        int
	kit           ui.Kit
	presentation  *appView
	viewEpoch     uint64

	mode            viewMode
	homeData        homeData
	homeSelected    int
	launcherQuery   string
	paletteOpen     bool
	paletteQuery    string
	paletteSelected int
	previousMode    viewMode
}

func (m *Model) currentMode() viewMode {
	if m.shelf {
		return modeLauncher
	}
	return m.mode
}

func (m *Model) setMode(mode viewMode) {
	m.mode = mode
	m.shelf = (mode == modeLauncher)
}

func (m *Model) Focus() FocusState {
	if m.paletteOpen {
		return FocusPalette
	}
	if m.presentation != nil {
		return FocusApp
	}
	switch m.currentMode() {
	case modeHome:
		return FocusHome
	case modeLauncher:
		return FocusLauncher
	default:
		return FocusPrompt
	}
}

func New(ctx context.Context, c Caller, closed <-chan struct{}) *Model {
	return NewWithTheme(ctx, c, closed, theme.Nostalgia(), ui.ANSI256)
}

// NewWithTheme makes appearance and color capability explicit. Plain rendering
// is useful for snapshots and terminals without ANSI styling.
func NewWithTheme(ctx context.Context, c Caller, closed <-chan struct{}, t theme.Theme, mode ui.ColorMode) *Model {
	return &Model{
		ctx: ctx, client: c, closed: closed, kit: ui.New(t, mode), width: 80, height: 24, cwd: "/users/guest",
		mode: modeHome,
		transcript: []entry{
			{"Welcome home. A familiar prompt. A whole new environment.", "accent"},
			{"Type help to explore, or F2 to open your app shelf.", "muted"},
		},
	}
}

func (m *Model) Init() tea.Cmd {
	m.busy = true // Initial discovery must finish before accepting a command.
	cwd := m.cwd
	refresh := func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		var apps []appStatus
		err := m.client.Call(ctx, "app/list", nil, &apps)
		msg := resultMsg{apps: apps, cwd: cwd, err: err}

		var status struct {
			UptimeSeconds float64 `json:"uptime_seconds"`
			User          string  `json:"user"`
			Processes     []any   `json:"processes"`
			Services      []any   `json:"services"`
		}
		if sErr := m.client.Call(ctx, "sys/status", nil, &status); sErr == nil {
			msg.status = sysStatusData{
				UptimeSeconds: status.UptimeSeconds,
				User:          status.User,
				ProcessCount:  len(status.Processes),
				ServicesCount: len(status.Services),
			}
			msg.hasStatus = true
		}

		var dir struct {
			Entries []struct {
				Name  string `json:"name"`
				IsDir bool   `json:"is_dir"`
				Size  int64  `json:"size"`
			} `json:"entries"`
		}
		if fErr := m.client.Call(ctx, "fs/list", map[string]string{"path": "/users/guest/documents"}, &dir); fErr == nil {
			var docs []docShortcut
			for _, e := range dir.Entries {
				if !e.IsDir {
					docs = append(docs, docShortcut{
						Name: e.Name,
						Path: "/users/guest/documents/" + e.Name,
						Size: e.Size,
					})
				}
			}
			msg.documents = docs
			msg.hasDocs = true
		}

		return msg
	}
	watch := func() tea.Msg {
		select {
		case <-m.closed:
		case <-m.ctx.Done():
		}
		return closedMsg{}
	}
	return tea.Batch(refresh, watch)
}

func (m *Model) submit(line string) tea.Cmd {
	m.busy = true
	m.scroll = 0
	m.append(entry{dosPath(m.cwd) + "> " + line, "command"})
	m.history = append(m.history, line)
	if len(m.history) > 100 {
		m.history = m.history[len(m.history)-100:]
	}
	m.historyPos = len(m.history)
	m.input, m.cursor, m.draft = nil, 0, ""
	cwd := m.cwd
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		return execute(ctx, m.client, cwd, line)
	}
}

func (m *Model) append(e entry) {
	// Never render terminal controls from app/file output (escape sequences,
	// OSC clipboard commands, carriage return spoofing, etc.).
	e.text = safe(e.text)
	// Keep memory bounded even when a service returns an enormous file.
	r := []rune(e.text)
	if len(r) > 12000 {
		e.text = string(r[:12000]) + "\n[output truncated]"
	}
	for _, line := range strings.Split(e.text, "\n") {
		m.transcript = append(m.transcript, entry{line, e.kind})
	}
	if len(m.transcript) > maxTranscript {
		m.transcript = m.transcript[len(m.transcript)-maxTranscript:]
	}
	if e.kind == "error" {
		if m.presentation == nil {
			m.setMode(modePrompt)
		}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case viewMsg, viewTickMsg, viewCheckMsg, viewCancelMsg:
		return m, m.updatePresentation(msg)
	case closedMsg:
		if m.presentation != nil && m.presentation.cancel != nil {
			m.presentation.cancel()
		}
		m.presentation = nil
		return m, tea.Quit
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case resultMsg:
		m.busy = false
		if msg.cwd != "" {
			m.cwd = msg.cwd
		}
		if msg.apps != nil {
			m.apps = msg.apps
			m.selected = min(m.selected, max(0, len(m.apps)-1))
		}
		if msg.hasStatus {
			m.homeData.UptimeSeconds = msg.status.UptimeSeconds
			m.homeData.User = msg.status.User
			m.homeData.ProcessCount = msg.status.ProcessCount
			m.homeData.ServicesCount = msg.status.ServicesCount
		}
		if msg.hasDocs {
			m.homeData.Documents = msg.documents
		}
		if msg.text != "" {
			m.append(entry{msg.text, "output"})
		}
		if msg.err != nil {
			m.append(entry{msg.err.Error(), "error"})
		}
		if msg.quit {
			return m, tea.Quit
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			return m, tea.Quit
		case "ctrl+p":
			if m.paletteOpen {
				m.closePalette()
			} else {
				m.openPalette()
			}
			return m, nil
		case "f1":
			if m.paletteOpen {
				m.closePalette()
			}
			cancel := m.dismissView()
			if m.currentMode() == modeHome {
				m.setMode(modePrompt)
			} else {
				m.setMode(modeHome)
			}
			m.scroll = 0
			return m, cancel
		case "f2":
			if m.paletteOpen {
				m.closePalette()
			}
			cancel := m.dismissView()
			if m.currentMode() == modeLauncher {
				m.setMode(modePrompt)
			} else {
				m.setMode(modeLauncher)
			}
			m.scroll = 0
			return m, cancel
		case "f4":
			if m.presentation == nil {
				return m, m.selectedView()
			}
			return m, nil
		case "esc":
			if m.paletteOpen {
				m.closePalette()
				return m, nil
			}
			if m.presentation != nil {
				return m, m.viewKey(msg)
			}
			if m.currentMode() == modeLauncher {
				return m.handleLauncherKey(msg)
			}
			m.setMode(modePrompt)
			return m, nil
		case "pgup":
			m.scroll += max(1, m.height/2)
			return m, nil
		case "pgdown":
			m.scroll = max(0, m.scroll-max(1, m.height/2))
			return m, nil
		}
		if m.paletteOpen {
			return m.handlePaletteKey(msg)
		}
		if m.busy {
			return m, nil
		}
		if m.presentation != nil {
			return m, m.viewKey(msg)
		}
		if m.currentMode() == modeLauncher {
			return m.handleLauncherKey(msg)
		}
		if m.currentMode() == modeHome {
			return m.handleHomeKey(msg)
		}
		return m.handlePromptKey(msg)
	}
	return m, nil
}

func (m *Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		line := strings.TrimSpace(string(m.input))
		if line == "" {
			return m, nil
		}
		if strings.EqualFold(line, "cls") || strings.EqualFold(line, "clear") {
			m.transcript = nil
			m.input = nil
			m.cursor = 0
			m.scroll = 0
			return m, nil
		}
		if strings.EqualFold(line, "home") {
			m.input = nil
			m.cursor = 0
			m.setMode(modeHome)
			return m, nil
		}
		if strings.EqualFold(line, "launcher") || strings.EqualFold(line, "shelf") {
			m.input = nil
			m.cursor = 0
			m.setMode(modeLauncher)
			return m, nil
		}
		if strings.EqualFold(line, "palette") {
			m.input = nil
			m.cursor = 0
			m.openPalette()
			return m, nil
		}
		return m, m.submit(line)
	case tea.KeyBackspace, tea.KeyCtrlH:
		if m.cursor > 0 {
			m.input = append(m.input[:m.cursor-1], m.input[m.cursor:]...)
			m.cursor--
		}
	case tea.KeyDelete:
		if m.cursor < len(m.input) {
			m.input = append(m.input[:m.cursor], m.input[m.cursor+1:]...)
		}
	case tea.KeyLeft:
		m.cursor = max(0, m.cursor-1)
	case tea.KeyRight:
		m.cursor = min(len(m.input), m.cursor+1)
	case tea.KeyHome, tea.KeyCtrlA:
		m.cursor = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		m.cursor = len(m.input)
	case tea.KeyCtrlU:
		m.input = nil
		m.cursor = 0
	case tea.KeyUp, tea.KeyDown:
		if len(m.history) == 0 {
			break
		}
		if m.historyPos == len(m.history) {
			m.draft = string(m.input)
		}
		if msg.Type == tea.KeyUp {
			m.historyPos = max(0, m.historyPos-1)
		} else {
			m.historyPos = min(len(m.history), m.historyPos+1)
		}
		text := m.draft
		if m.historyPos < len(m.history) {
			text = m.history[m.historyPos]
		}
		m.input = []rune(text)
		m.cursor = len(m.input)
	case tea.KeyTab:
		m.complete()
	case tea.KeyRunes, tea.KeySpace:
		if len(m.input) == 0 && msg.Type == tea.KeyRunes && string(msg.Runes) == "/" {
			m.openPalette()
			return m, nil
		}
		text := safe(string(msg.Runes))
		if msg.Type == tea.KeySpace {
			text = " "
		}
		text = strings.ReplaceAll(text, "\n", " ")
		if len(m.input)+len([]rune(text)) <= 4096 {
			tail := append([]rune(nil), m.input[m.cursor:]...)
			m.input = append(append(m.input[:m.cursor], []rune(text)...), tail...)
			m.cursor += len([]rune(text))
		}
	}
	return m, nil
}

func (m *Model) complete() {
	prefix := string(m.input)
	choices := []string{"help", "apps", "launch", "run", "stop", "echo", "call", "dir", "ls", "cd", "type", "cat", "ps", "logs", "log", "status", "cls", "exit", "shutdown", "home", "palette"}
	if verb, partial, ok := strings.Cut(prefix, " "); ok {
		if verb != "launch" && verb != "run" && verb != "stop" {
			return
		}
		choices = nil
		for _, a := range m.apps {
			if strings.HasPrefix(a.Manifest.ID, partial) {
				choices = append(choices, verb+" "+a.Manifest.ID)
			}
		}
	}
	var matches []string
	for _, c := range choices {
		if strings.HasPrefix(c, prefix) {
			matches = append(matches, c)
		}
	}
	sort.Strings(matches)
	if len(matches) == 1 {
		m.input = []rune(matches[0])
		m.cursor = len(m.input)
	}
}

func safe(s string) string { return ui.Sanitize(s) }

func dosPath(p string) string { return "C:" + strings.ReplaceAll(p, "/", `\`) }

func (m *Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	bounds := ui.Bounds{Width: m.width, Height: m.height}
	if m.width < 30 || m.height < 10 {
		return ui.Fit(m.kit.Text("GOSTALGIA\nResize to 30×10 or larger.\nCtrl-C exits."), bounds)
	}
	panelBounds := ui.Bounds{Width: m.width, Height: m.height - 1}
	w := m.kit.PanelContentBounds(panelBounds).Width

	active := 0
	switch m.currentMode() {
	case modeHome:
		active = 0
	case modeLauncher:
		active = 1
	case modePrompt:
		active = 2
	}
	tabItems := []ui.Tab{{Label: "Home"}, {Label: "Apps"}, {Label: "Prompt"}}
	if m.presentation != nil {
		tabItems = append(tabItems, ui.Tab{Label: viewText(m.presentation.data.Title)})
		active = 3
	}
	tabs := m.kit.Tabs(tabItems, active, w)
	badge := m.kit.Badge("ONLINE", theme.Success, w) + m.kit.Muted("   guest · C: environment drive")
	bodyHeight := max(1, m.height-9)
	var lines []string

	if m.paletteOpen {
		lines = m.renderPalette(w, bodyHeight)
	} else if m.presentation != nil {
		lines = m.viewLines(bodyHeight)
		for i, line := range lines {
			lines[i] = ui.Truncate(line, w)
		}
	} else {
		switch m.currentMode() {
		case modeLauncher:
			lines = m.renderLauncher(w, bodyHeight)
		case modeHome:
			lines = m.renderHome(w, bodyHeight)
		default:
			for _, e := range m.transcript {
				line := ui.Wrap(e.text, w)
				switch e.kind {
				case "accent":
					line = m.kit.Heading(line)
				case "muted":
					line = m.kit.Muted(line)
				case "command":
					line = m.kit.Heading(line)
				case "error":
					line = m.kit.StatusText(line, theme.Error)
				default:
					line = m.kit.Text(line)
				}
				lines = append(lines, strings.Split(line, "\n")...)
			}
		}
	}

	if m.paletteOpen || m.currentMode() == modeLauncher || m.currentMode() == modeHome || m.presentation != nil {
		if len(lines) > bodyHeight {
			lines = lines[:bodyHeight]
		}
	} else {
		end := max(0, len(lines)-min(m.scroll, max(0, len(lines)-bodyHeight)))
		start := max(0, end-bodyHeight)
		lines = lines[start:end]
	}
	body := ui.Fit(strings.Join(lines, "\n"), ui.Bounds{Width: w, Height: bodyHeight})

	promptPath := safe(dosPath(m.cwd))
	if lipgloss.Width(promptPath) > w/2 {
		promptPath = "C:…" + ui.Tail(promptPath, max(1, w/2-3))
	}
	prompt := m.kit.Heading(promptPath + "> ")
	if m.paletteOpen {
		prompt = m.kit.Heading("PALETTE> ") + m.kit.Muted("Type to filter · ↑↓ navigate · Enter execute · Esc close")
	} else if m.presentation != nil {
		prompt += m.kit.Muted("App view owns focus. Esc returns to prompt.")
	} else if m.busy {
		prompt += m.kit.Badge("", theme.Busy, max(0, w-lipgloss.Width(prompt)))
	} else if m.currentMode() == modeLauncher {
		filtered := m.filteredApps()
		if len(filtered) > 0 && m.selected < len(filtered) && filtered[m.selected].Running {
			prompt += m.kit.StatusText(fmt.Sprintf("%s is LIVE (PID %d). Enter relaunch, F3 stop, F4 view.", filtered[m.selected].Manifest.Name, filtered[m.selected].PID), theme.Success)
		} else if len(filtered) > 0 && m.selected < len(filtered) {
			prompt += m.kit.Muted(fmt.Sprintf("Enter launches %s. F3 stop · F4 view · Esc prompt.", filtered[m.selected].Manifest.Name))
		} else {
			prompt += m.kit.Muted("Esc returns to prompt.")
		}
	} else if m.currentMode() == modeHome {
		prompt += m.kit.Muted("Home view owns focus. Esc returns to prompt · Ctrl-P palette.")
	} else {
		// Show a cursor-centered slice rather than allowing long pasted input to
		// push the prompt off screen. Cell width, not bytes, controls the slice.
		room := max(1, w-lipgloss.Width(prompt)-1)
		before := ui.Tail(string(m.input[:m.cursor]), room/2)
		after := ui.Fit(string(m.input[m.cursor:]), ui.Bounds{Width: room - lipgloss.Width(before), Height: 1})
		prompt += m.kit.Text(before) + m.kit.Selection(" ") + m.kit.Text(after)
	}
	var bindings []ui.Binding
	if m.paletteOpen {
		items := m.filteredPaletteItems()
		bindings = []ui.Binding{
			{Key: "Esc", Help: "CLOSE"},
			{Key: "Enter", Help: "EXECUTE", Disabled: len(items) == 0},
			{Key: "↑↓", Help: "NAVIGATE", Disabled: len(items) == 0},
			{Key: "Ctrl-C", Help: "EXIT"},
		}
	} else if m.presentation != nil {
		v := m.presentation
		blocked := v.busy || v.instance == "" || v.data.State == sdk.ViewLoading
		action := max(0, v.focus-len(v.data.Fields))
		actionBlocked := blocked || action >= len(v.data.Actions) || v.data.Actions[action].Disabled
		bindings = []ui.Binding{
			{Key: "Esc", Help: "CANCEL/BACK"}, {Key: "Ctrl-C", Help: "EXIT"},
			{Key: "Tab", Help: "FOCUS", Disabled: blocked}, {Key: "Enter", Help: "ACTION", Disabled: actionBlocked},
			{Key: "↑↓", Help: "ITEM", Disabled: blocked}, {Key: "F2", Help: "APPS"},
		}
	} else if m.currentMode() == modeLauncher {
		filtered := m.filteredApps()
		bindings = []ui.Binding{
			{Key: "Esc", Help: "PROMPT"},
			{Key: "Enter", Help: "RUN", Disabled: m.busy || len(filtered) == 0},
			{Key: "F3", Help: "STOP", Disabled: m.busy || len(filtered) == 0},
			{Key: "F4", Help: "VIEW", Disabled: m.busy || len(filtered) == 0},
			{Key: "↑↓", Help: "SELECT", Disabled: m.busy || len(filtered) == 0},
			{Key: "Ctrl-P", Help: "PALETTE"},
		}
	} else if m.currentMode() == modeHome {
		items := m.homeItems()
		bindings = []ui.Binding{
			{Key: "F1", Help: "PROMPT"},
			{Key: "F2", Help: "APPS"},
			{Key: "Ctrl-P", Help: "PALETTE"},
			{Key: "↑↓", Help: "SELECT", Disabled: len(items) == 0},
			{Key: "Enter", Help: "OPEN", Disabled: len(items) == 0},
			{Key: "Ctrl-C", Help: "EXIT"},
		}
	} else {
		bindings = []ui.Binding{
			{Key: "Ctrl-C", Help: "EXIT"},
			{Key: "F1", Help: "HOME"},
			{Key: "F2", Help: "APPS"},
			{Key: "Tab", Help: "COMPLETE", Disabled: m.busy},
			{Key: "↑↓", Help: "HISTORY", Disabled: m.busy},
			{Key: "Ctrl-P", Help: "PALETTE"},
		}
	}

	content := strings.Join([]string{tabs, ui.Truncate(badge, w), "", body, "", ui.Truncate(prompt, w)}, "\n")
	return m.kit.Panel(ui.Panel{Title: "G O S T A L G I A  /  PERSONAL COMPUTING, REIMAGINED", Body: content},
		panelBounds) + "\n" + m.kit.HelpBar(bindings, m.width)
}

// Run takes over the terminal, restoring it on every exit. Passing options is
// useful for tests; production runs in the alternate screen with bracketed paste.
func Run(ctx context.Context, c Caller, closed <-chan struct{}, options ...tea.ProgramOption) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx)}
	opts = append(opts, options...)
	model := New(ctx, c, closed)
	_, err := tea.NewProgram(model, opts...).Run()
	if cleanup := model.dismissView(); cleanup != nil {
		cleanup()
	}
	return err
}

// Keep the public boundary small: no runtime imports and no terminal IO in apps.
var _ tea.Model = (*Model)(nil)
