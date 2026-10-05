package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gostalgia/internal/ipc"
	"gostalgia/internal/runtime"
	"gostalgia/platform"
)

type noopCaller struct{}

func (noopCaller) Call(context.Context, string, any, any) error { return nil }

func TestEditingHistoryCompletionAndBounds(t *testing.T) {
	m := New(context.Background(), noopCaller{}, nil)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("héllo")})
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if string(m.input) != "hélo" {
		t.Fatalf("edited = %s", string(m.input))
	}
	m.history = []string{"apps", "echo hello"}
	m.historyPos = 2
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if string(m.input) != "echo hello" {
		t.Fatal("history up failed")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if string(m.input) != "hélo" {
		t.Fatal("draft not restored")
	}
	m.input = []rune("laun")
	m.cursor = 4
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if string(m.input) != "launch" {
		t.Fatal("completion failed")
	}
	m.append(entry{strings.Repeat("line\n", 1000) + "\x1b]52;c;unsafe\a", "output"})
	if len(m.transcript) != maxTranscript {
		t.Fatalf("scrollback = %d", len(m.transcript))
	}
	m.cwd = "/users/guest/" + strings.Repeat("long-directory/", 20)
	m.input = []rune(strings.Repeat("界", 100))
	m.cursor = 90
	for _, size := range [][2]int{{80, 24}, {40, 12}, {30, 10}, {20, 8}, {120, 40}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := m.View()
		if strings.Contains(view, "\a") {
			t.Fatal("rendered terminal control")
		}
		if lipgloss.Height(view) > size[1] || lipgloss.Width(view) > size[0] {
			t.Fatalf("view %dx%d exceeds %dx%d", lipgloss.Width(view), lipgloss.Height(view), size[0], size[1])
		}
	}
}

func TestShelfKeepsSelectedAppVisible(t *testing.T) {
	m := New(context.Background(), noopCaller{}, nil)
	m.shelf = true
	m.height = 12
	m.width = 60
	for i := 0; i < 20; i++ {
		var a appStatus
		a.Manifest.Name = fmt.Sprintf("APP-%02d", i)
		m.apps = append(m.apps, a)
	}
	m.selected = 19
	if !strings.Contains(m.View(), "APP-19") {
		t.Fatal("selected app offscreen")
	}
}

func TestDOSPathsAndParsing(t *testing.T) {
	for input, want := range map[string]string{`C:\users\guest`: "/users/guest", `documents\note.txt`: "/users/guest/documents/note.txt", "..": "/users"} {
		if got := envPath("/users/guest", input); got != want {
			t.Errorf("%s = %s, want %s", input, got, want)
		}
	}
	args, err := words(`type "C:\a folder\file.txt"`)
	if err != nil || len(args) != 2 || args[1] != `C:\a folder\file.txt` {
		t.Fatalf("words = %v, %v", args, err)
	}
	if _, err := words(`echo "unfinished`); err == nil {
		t.Fatal("unclosed quote accepted")
	}
	for _, line := range []string{"launch", "stop", "cat", "cd", "ps extra", "call route {no}", "missing"} {
		if _, _, _, err := command(context.Background(), noopCaller{}, "/", line); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
}

// observedModel runs the actual tea event loop while providing an explicit
// completion barrier; tests never read UI state concurrently with Update.
type observedModel struct {
	*Model
	results chan resultMsg
}

func (m *observedModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.Model.Update(msg)
	if r, ok := msg.(resultMsg); ok {
		m.results <- r
	}
	return m, cmd
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

func TestBubbleTeaSocketAppLifecycle(t *testing.T) {
	root := t.TempDir()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: root, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Shutdown("shell test cleanup") })
	data, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	conn, err := platform.DialIPC(rt.Endpoint())
	if err != nil {
		t.Fatal(err)
	}
	client, err := ipc.NewClient(conn, info.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m := &observedModel{Model: New(ctx, client, rt.Done()), results: make(chan resultMsg, 20)}
	output := &lockedBuffer{}
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(output), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	t.Cleanup(func() { p.Kill(); <-done })
	await := func() resultMsg {
		t.Helper()
		select {
		case r := <-m.results:
			if r.err != nil {
				t.Fatal(r.err)
			}
			return r
		case <-ctx.Done():
			t.Fatal("Bubble Tea result timed out")
			return resultMsg{}
		}
	}
	await() // Init's app list.
	p.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	submit := func(line string) resultMsg {
		t.Helper()
		p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(line)})
		p.Send(tea.KeyMsg{Type: tea.KeyEnter})
		return await()
	}
	if r := submit("apps"); !strings.Contains(r.text, "com.gostalgia.echo") {
		t.Fatal("demo missing")
	}
	if r := submit("echo fabulous"); r.text != "fabulous   [echo #1]" {
		t.Fatal(r.text)
	}
	r := submit("call app/com.gostalgia.echo/identity")
	var identity struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(r.text), &identity); err != nil {
		t.Fatal(err)
	}
	if len(identity.Capabilities) != 1 || identity.Capabilities[0] != "ipc" {
		t.Fatalf("demo grant = %v", identity.Capabilities)
	}
	submit("stop com.gostalgia.echo")
	if rt.Apps.IsRunning("com.gostalgia.echo") {
		t.Fatal("stop left app running")
	}
	submit("launch com.gostalgia.echo")
	if !rt.Apps.IsRunning("com.gostalgia.echo") {
		t.Fatal("launch failed")
	}
	if r := submit("echo renewed"); !strings.Contains(r.text, "echo #1") {
		t.Fatal("instance state not reset")
	}
	if r := submit(`cd C:\users\guest\documents`); r.cwd != "/users/guest/documents" {
		t.Fatal(r.cwd)
	}
	submit("dir")
	p.Send(tea.KeyMsg{Type: tea.KeyF2})
	p.Send(tea.KeyMsg{Type: tea.KeyF3})
	await() // stop through the app shelf
	p.Send(tea.KeyMsg{Type: tea.KeyEnter})
	await() // relaunch through the app shelf
	p.Send(tea.KeyMsg{Type: tea.KeyEsc})
	submit("exit")
	// Bubble Tea flushes its renderer on quit; verify the actual rendered UI.
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		// Replace the cleanup's consumed channel result.
		done <- nil
	case <-ctx.Done():
		t.Fatal("program did not exit")
	}
	if !strings.Contains(output.String(), "G O S T A L G I A") {
		t.Fatal("Bubble Tea did not render the styled shell")
	}
}
