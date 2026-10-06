package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

func TestTruncateGraphemeCells(t *testing.T) {
	for _, tc := range []struct {
		text  string
		width int
		want  string
	}{
		{"hello", 4, "hel…"},
		{"hello", 1, "…"},
		{"hello", 0, ""},
		{"界界A", 4, "界…"},
		{"e\u0301clair", 2, "e\u0301…"},
		{"👩‍💻x", 2, "…"},
		{"👩‍💻x", 3, "👩‍💻x"},
		{"🇺🇸xy", 3, "🇺🇸…"},
		{"abc\nsecond", 8, "abc"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if got := Truncate(tc.text, tc.width); got != tc.want {
				t.Fatalf("Truncate(%q, %d) = %q, want %q", tc.text, tc.width, got, tc.want)
			}
		})
	}
}

func TestANSILayoutPreservesStyles(t *testing.T) {
	text := "\x1b[31m界e\u0301👩‍💻x\x1b[0m"
	got := Truncate(text, 4)
	if ansi.Strip(got) != "界e\u0301…" || !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("styled truncation = %q", got)
	}
	assertRectangle(t, Fit(text+"\nsecond", Bounds{4, 3}), Bounds{4, 3})
	for _, width := range []int{1, 2, 3, 4, 80} {
		for _, line := range strings.Split(Wrap(text, width), "\n") {
			if ansi.StringWidth(line) > width || !utf8.ValidString(line) {
				t.Fatalf("wrapped line %q exceeds %d cells", line, width)
			}
			if strings.Contains(ansi.Strip(line), "👩") && !strings.Contains(ansi.Strip(line), "👩‍💻") {
				t.Fatalf("split emoji cluster: %q", line)
			}
		}
	}
	if got := ansi.Strip(Tail(text, 3)); got != "👩‍💻x" {
		t.Fatalf("Tail = %q", got)
	}
}

func TestFitAndWrap(t *testing.T) {
	if got := Fit("界界A\nsecond", Bounds{3, 3}); got != "界 \nsec\n   " {
		t.Fatalf("Fit = %q", got)
	}
	if got := Wrap("e\u0301界👩‍💻Z", 3); got != "e\u0301界\n👩‍💻Z" {
		t.Fatalf("Wrap = %q", got)
	}
	for _, bounds := range []Bounds{{0, 3}, {3, 0}, {-1, 3}, {3, -1}} {
		if got := Fit("text", bounds); got != "" {
			t.Fatalf("nonpositive bounds %+v = %q", bounds, got)
		}
	}
	if Wrap("text", 0) != "" || Tail("text", 0) != "" {
		t.Fatal("zero-width layout should be empty")
	}
}

func TestSanitizeExternalText(t *testing.T) {
	text := "\x1b[31mred\x1b[0m\x1b]52;c;clipboard\a\x1bPpayload\x1b\\\r\nnext\t\x00\x7f"
	if got := Sanitize(text); got != "red\nnext " {
		t.Fatalf("Sanitize = %q", got)
	}
	if got := Sanitize("e\u0301 👩‍💻 界"); got != "e\u0301 👩‍💻 界" {
		t.Fatalf("sanitizing changed printable clusters: %q", got)
	}
	if !utf8.ValidString(Sanitize("\xff")) {
		t.Fatal("invalid UTF-8 was not repaired")
	}
}

func assertRectangle(t *testing.T, view string, bounds Bounds) {
	t.Helper()
	if bounds.Width <= 0 || bounds.Height <= 0 {
		if view != "" {
			t.Fatalf("expected empty rectangle %+v, got %q", bounds, view)
		}
		return
	}
	lines := strings.Split(view, "\n")
	if len(lines) != bounds.Height {
		t.Fatalf("height = %d, want %d\n%q", len(lines), bounds.Height, view)
	}
	for _, line := range lines {
		if got := ansi.StringWidth(line); got != bounds.Width {
			t.Fatalf("line width = %d, want %d: %q", got, bounds.Width, line)
		}
		if !utf8.ValidString(line) {
			t.Fatalf("invalid UTF-8: %q", line)
		}
	}
}
