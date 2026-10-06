// Package ui is Gostalgia's stateless Charm component kit. Views pass their theme,
// color mode, state, and outer bounds explicitly. Rendering starts no timers,
// performs no IO, and never uses Lip Gloss's global renderer.
package ui

import (
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"gostalgia/internal/experience/theme"
)

type ColorMode uint8

const (
	Plain ColorMode = iota
	ANSI256
	TrueColor
)

// Kit owns an isolated renderer and a value copy of its theme. It can be reused
// concurrently; its rendering methods do not mutate either.
type Kit struct {
	theme    theme.Theme
	mode     ColorMode
	renderer *lipgloss.Renderer
}

func New(t theme.Theme, mode ColorMode) Kit {
	profile := termenv.Ascii
	switch mode {
	case ANSI256:
		profile = termenv.ANSI256
	case TrueColor:
		profile = termenv.TrueColor
	default:
		mode = Plain
	}
	r := lipgloss.NewRenderer(io.Discard, termenv.WithProfile(profile))
	r.SetColorProfile(profile)
	r.SetHasDarkBackground(false)
	return Kit{theme: t, mode: mode, renderer: r}
}

func (k Kit) paint(text string, fg, bg lipgloss.Color, bold bool) string {
	if text == "" || k.mode == Plain {
		return text
	}
	style := k.renderer.NewStyle().Foreground(fg).Background(bg).Bold(bold)
	// Lip Gloss doesn't restore an outer style after a nested full SGR reset.
	// Reapply it so composed badges/tabs/help don't expose the host background
	// in separators or padding. Derive the prefix from this explicit renderer.
	if strings.Contains(text, "\x1b[0m") || strings.Contains(text, "\x1b[m") {
		prefix := strings.TrimSuffix(style.Render(""), "\x1b[0m")
		text = strings.NewReplacer(
			"\x1b[0m", "\x1b[0m"+prefix,
			"\x1b[m", "\x1b[m"+prefix,
		).Replace(text)
	}
	return style.Render(text)
}

// Text styles trusted text. Use Sanitize first for file, app, or IPC data.
func (k Kit) Text(text string) string {
	p := k.theme.Palette
	return k.paint(text, p.Text, p.Surface, false)
}

func (k Kit) Heading(text string) string {
	p := k.theme.Palette
	return k.paint(text, p.Accent, p.Surface, k.theme.Typography.HeadingBold)
}

func (k Kit) Muted(text string) string {
	p := k.theme.Palette
	return k.paint(text, p.Muted, p.Surface, false)
}

func (k Kit) StatusText(text string, state theme.State) string {
	s := k.theme.Status(state)
	return k.paint(text, s.Text, s.Fill, false)
}

func (k Kit) Selection(text string) string {
	f := k.theme.Focus
	return k.paint(text, f.Text, f.Fill, k.theme.Typography.HeadingBold)
}

func (k Kit) FocusText(text string) string {
	return k.Selection(k.theme.Focus.Marker + " " + text)
}
