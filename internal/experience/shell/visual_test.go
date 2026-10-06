package shell

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"gostalgia/internal/experience/theme"
	"gostalgia/internal/experience/ui"
)

var updateVisual = flag.Bool("update-visual", false, "update shell visual golden snapshots")

func TestShellVisualSnapshots(t *testing.T) {
	for _, view := range []string{"home", "shelf", "empty", "busy", "error"} {
		t.Run(view, func(t *testing.T) {
			m := NewWithTheme(context.Background(), noopCaller{}, nil, theme.Nostalgia(), ui.Plain)
			if view == "shelf" || view == "empty" {
				m.shelf = true
			}
			if view == "shelf" {
				var app appStatus
				app.Manifest.Name = "Echo"
				app.Manifest.ID = "com.gostalgia.echo"
				app.Manifest.Version = "0.1.0"
				app.Manifest.Description = "A friendly first app."
				app.Manifest.Permissions = []string{"ipc"}
				app.Running, app.PID = true, 1
				m.apps = []appStatus{app}
			}
			m.busy = view == "busy"
			if view == "error" {
				m.append(entry{"The app couldn't start. Try again.", "error"})
			}
			rendered := strings.Split(m.View(), "\n")
			for i := range rendered {
				rendered[i] = strings.TrimRight(rendered[i], " ")
			}
			got := strings.Join(rendered, "\n") + "\n"
			path := filepath.Join("testdata", view+".golden")
			if *updateVisual {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("shell visual snapshot changed:\n%s", got)
			}
		})
	}
}

func TestShellExplicitThemeAndViewport(t *testing.T) {
	for _, mode := range []ui.ColorMode{ui.Plain, ui.ANSI256, ui.TrueColor} {
		warm := NewWithTheme(context.Background(), noopCaller{}, nil, theme.Nostalgia(), mode)
		dark := NewWithTheme(context.Background(), noopCaller{}, nil, theme.Midnight(), mode)
		if ansi.Strip(warm.View()) != ansi.Strip(dark.View()) {
			t.Fatal("theme selection changed shell layout")
		}
		if mode == ui.Plain && strings.Contains(warm.View(), "\x1b") {
			t.Fatal("plain shell contains ANSI")
		}
		if mode != ui.Plain && warm.View() == dark.View() {
			t.Fatal("shell ignored explicit palette")
		}
		warm.cwd = "/users/guest/" + strings.Repeat("界e\u0301👩‍💻/", 50)
		warm.input = []rune(strings.Repeat("e\u0301👩‍💻界", 100))
		warm.cursor = len(warm.input)
		for _, shelf := range []bool{false, true} {
			warm.shelf = shelf
			for _, size := range []ui.Bounds{
				{Width: 0, Height: 0}, {Width: 1, Height: 1}, {Width: 20, Height: 8},
				{Width: 30, Height: 10}, {Width: 80, Height: 24}, {Width: 120, Height: 40}, {Width: 240, Height: 80},
			} {
				warm.Update(tea.WindowSizeMsg{Width: size.Width, Height: size.Height})
				view := warm.View()
				if size.Width == 0 || size.Height == 0 {
					if view != "" {
						t.Fatal("zero viewport was not empty")
					}
					continue
				}
				if lipgloss.Width(view) != size.Width || lipgloss.Height(view) != size.Height {
					t.Fatalf("shell dimensions = %dx%d, want %dx%d", lipgloss.Width(view), lipgloss.Height(view), size.Width, size.Height)
				}
			}
		}
	}
}
