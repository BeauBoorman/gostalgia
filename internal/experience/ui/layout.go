package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Bounds is the total outer size of a component in terminal cells. Nonpositive
// dimensions render nothing. Components never impose a minimum viewport.
type Bounds struct {
	Width, Height int
}

// GraphemeClusters decomposes s into its user-perceived Unicode grapheme clusters.
// It keeps combining characters, zero-width joiners, flags, and multi-column runes intact.
func GraphemeClusters(s string) []string {
	var clusters []string
	for len(s) > 0 {
		cluster, _ := ansi.FirstGraphemeCluster(s, ansi.WcWidth)
		if len(cluster) == 0 {
			_, size := utf8.DecodeRuneInString(s)
			if size <= 0 {
				break
			}
			cluster = s[:size]
		}
		clusters = append(clusters, cluster)
		s = s[len(cluster):]
	}
	return clusters
}

// GraphemeWidth returns the monospace terminal cell width of s using wcwidth.
func GraphemeWidth(s string) int {
	return ansi.StringWidth(s)
}

// Sanitize removes terminal sequences and controls from external text. Newlines
// are preserved and tabs expand to the next 8-cell tab stop so preformatted,
// tab-indented text keeps its alignment. Rendered component output is trusted
// ANSI; don't sanitize it a second time when composing panels.
func Sanitize(s string) string {
	s = ansi.Strip(strings.ToValidUTF8(s, "�"))
	var b strings.Builder
	b.Grow(len(s))
	col := 0
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
			col = 0
		case r == '\t':
			n := 8 - col%8
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
			col += ansi.StringWidth(string(r))
		}
	}
	return b.String()
}

func label(s string) string {
	return strings.ReplaceAll(Sanitize(s), "\n", " ")
}

// Truncate clips one trusted, possibly ANSI-styled line by grapheme cell width.
// An ellipsis is included only when the line doesn't fit.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s, _, _ = strings.Cut(s, "\n")
	return ansi.Truncate(s, width, "…")
}

// Tail keeps the rightmost cells without splitting a grapheme.
func Tail(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s, _, _ = strings.Cut(s, "\n")
	return ansi.TruncateLeft(s, max(0, ansi.StringWidth(s)-width), "")
}

// Wrap hard-wraps trusted text without splitting ANSI sequences or graphemes.
func Wrap(s string, width int) string {
	if width <= 0 {
		return ""
	}
	// A grapheme wider than the viewport cannot be wrapped. Clip such lines.
	lines := strings.Split(ansi.Hardwrap(s, width, true), "\n")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	return strings.Join(lines, "\n")
}

// Fit clips and pads trusted text to an exact rectangle. It does not wrap.
func Fit(s string, bounds Bounds) string {
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	out := make([]string, bounds.Height)
	for i := range out {
		var line string
		if i < len(lines) {
			line = ansi.Truncate(lines[i], bounds.Width, "")
		}
		out[i] = line + strings.Repeat(" ", max(0, bounds.Width-ansi.StringWidth(line)))
	}
	return strings.Join(out, "\n")
}
