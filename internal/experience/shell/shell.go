// Package shell is Gostalgia's Charm experience layer. Runtime and SDK never
// import it. All environment operations use Caller, not privileged internals.
package shell

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const maxTranscript = 400

type entry struct {
	text string
	kind string
}
type closedMsg struct{}

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
}

func New(ctx context.Context, c Caller, closed <-chan struct{}) *Model {
	return &Model{ctx: ctx, client: c, closed: closed, width: 80, height: 24, cwd: "/users/guest", transcript: []entry{
		{"Welcome home. A familiar prompt. A whole new environment.", "accent"},
		{"Type help to explore, or F2 to open your app shelf.", "muted"},
	}}
}

func (m *Model) Init() tea.Cmd {
	m.busy = true // Initial discovery must finish before accepting a command.
	cwd := m.cwd
	refresh := func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		var apps []appStatus
		err := m.client.Call(ctx, "app/list", nil, &apps)
		return resultMsg{apps: apps, cwd: cwd, err: err}
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
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case closedMsg:
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
		case "f2":
			m.shelf = !m.shelf
			m.scroll = 0
			return m, nil
		case "esc":
			m.shelf = false
			return m, nil
		case "pgup":
			m.scroll += max(1, m.height/2)
			return m, nil
		case "pgdown":
			m.scroll = max(0, m.scroll-max(1, m.height/2))
			return m, nil
		}
		if m.busy {
			return m, nil
		}
		if m.shelf {
			switch msg.String() {
			case "up":
				m.selected = max(0, m.selected-1)
			case "down":
				m.selected = min(max(0, len(m.apps)-1), m.selected+1)
			case "enter", "f3":
				if len(m.apps) > 0 {
					verb := "launch"
					if msg.String() == "f3" {
						verb = "stop"
					}
					return m, m.submit(verb + " " + m.apps[m.selected].Manifest.ID)
				}
			}
			return m, nil
		}
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
	}
	return m, nil
}

func (m *Model) complete() {
	prefix := string(m.input)
	choices := []string{"help", "apps", "launch", "run", "stop", "echo", "call", "dir", "ls", "cd", "type", "cat", "ps", "logs", "log", "status", "cls", "exit", "shutdown"}
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

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\].*?(\x07|\x1b\\)`)

func safe(s string) string {
	s = ansiRegex.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func dosPath(p string) string { return "C:" + strings.ReplaceAll(p, "/", `\`) }

var (
	frame         = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62"))
	accent        = lipgloss.NewStyle().Foreground(lipgloss.Color("117")).Bold(true)
	muted         = lipgloss.NewStyle().Foreground(lipgloss.Color("103"))
	gold          = lipgloss.NewStyle().Foreground(lipgloss.Color("221")).Bold(true)
	bad           = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62"))
)

func (m *Model) View() string {
	w := max(1, m.width-4)
	if m.width < 30 || m.height < 10 {
		return lipgloss.NewStyle().MaxWidth(max(1, m.width)).MaxHeight(max(1, m.height)).Render("GOSTALGIA\nResize to 30×10 or larger.\nCtrl-C exits.")
	}
	header := accent.Render("G O S T A L G I A") + muted.Render("  /  PERSONAL COMPUTING, REIMAGINED")
	badge := gold.Render("● ONLINE") + muted.Render("   guest · C: environment drive")
	bodyHeight := max(1, m.height-9)
	var lines []string
	if m.shelf {
		lines = append(lines, gold.Render("APP SHELF"), muted.Render("Enter launch · F3 stop · Esc prompt"))
		visible := max(1, bodyHeight-6)
		start := max(0, m.selected-visible+1)
		end := min(len(m.apps), start+visible)
		for i := start; i < end; i++ {
			a := m.apps[i]
			state := "READY"
			if a.Running {
				state = fmt.Sprintf("LIVE / PID %d", a.PID)
			}
			line := fmt.Sprintf("%s  %s  v%s  [%s]", a.Manifest.Name, a.Manifest.ID, a.Manifest.Version, state)
			if i == m.selected {
				line = selectedStyle.Render("› " + safe(line))
			} else {
				line = "  " + safe(line)
			}
			lines = append(lines, line)
		}
		if len(m.apps) == 0 {
			lines = append(lines, "No apps installed.")
		}
		if len(m.apps) > 0 {
			a := m.apps[m.selected]
			lines = append(lines, "", safe(a.Manifest.Description), muted.Render("GRANT  "+safe(strings.Join(a.Manifest.Permissions, " · "))))
		}
	} else {
		for _, e := range m.transcript {
			line := lipgloss.NewStyle().Width(w).Render(e.text)
			switch e.kind {
			case "accent":
				line = accent.Render(line)
			case "muted":
				line = muted.Render(line)
			case "command":
				line = gold.Render(line)
			case "error":
				line = bad.Render(line)
			}
			lines = append(lines, strings.Split(line, "\n")...)
		}
	}
	if m.shelf {
		if len(lines) > bodyHeight {
			lines = lines[:bodyHeight]
		}
	} else {
		end := max(0, len(lines)-min(m.scroll, max(0, len(lines)-bodyHeight)))
		start := max(0, end-bodyHeight)
		lines = lines[start:end]
	}
	body := lipgloss.NewStyle().Width(w).Height(bodyHeight).MaxHeight(bodyHeight).MaxWidth(w).Render(strings.Join(lines, "\n"))
	promptPath := safe(dosPath(m.cwd))
	if lipgloss.Width(promptPath) > w/2 {
		runes := []rune(promptPath)
		for len(runes) > 0 && lipgloss.Width(string(runes)) > max(1, w/2-4) {
			runes = runes[1:]
		}
		promptPath = "C:…" + string(runes)
	}
	prompt := gold.Render(promptPath + "> ")
	if m.busy {
		prompt += muted.Render("working…")
	} else {
		// Show a cursor-centered slice rather than allowing long pasted input to
		// push the prompt off screen. Cell width, not bytes, controls the slice.
		room := max(1, w-lipgloss.Width(prompt)-1)
		before := m.input[:m.cursor]
		for len(before) > 0 && lipgloss.Width(string(before)) > room/2 {
			before = before[1:]
		}
		after := m.input[m.cursor:]
		for len(after) > 0 && lipgloss.Width(string(before)+string(after)) > room {
			after = after[:len(after)-1]
		}
		prompt += string(before) + selectedStyle.Render(" ") + string(after)
	}
	footer := muted.Render("F2 APPS  ·  TAB COMPLETE  ·  ↑↓ HISTORY  ·  PgUp SCROLL  ·  Ctrl-C EXIT")
	clip := lipgloss.NewStyle().MaxWidth(w).MaxHeight(1)
	content := strings.Join([]string{clip.Render(header), clip.Render(badge), "", body, "", clip.Render(prompt)}, "\n")
	return frame.Width(w).MaxWidth(m.width).Render(content) + "\n" + lipgloss.NewStyle().MaxWidth(m.width).Render(footer)
}

// Run takes over the terminal, restoring it on every exit. Passing options is
// useful for tests; production runs in the alternate screen with bracketed paste.
func Run(ctx context.Context, c Caller, closed <-chan struct{}, options ...tea.ProgramOption) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx)}
	opts = append(opts, options...)
	_, err := tea.NewProgram(New(ctx, c, closed), opts...).Run()
	return err
}

// Keep the public boundary small: no runtime imports and no terminal IO in apps.
var _ tea.Model = (*Model)(nil)
