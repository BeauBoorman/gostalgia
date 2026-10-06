// Package theme defines Gostalgia's visual tokens as explicit, copyable values.
// It never inspects the host terminal or chooses colors from its background.
package theme

import "github.com/charmbracelet/lipgloss"

type State string

const (
	Normal   State = "normal"
	Disabled State = "disabled"
	Busy     State = "busy"
	Success  State = "success"
	Error    State = "error"
	Empty    State = "empty"
)

type Palette struct {
	Canvas, Surface, Text, Muted, Accent, Border lipgloss.Color
	Header, HeaderText                           lipgloss.Color
}

// Spacing values are terminal cells, not bytes or runes.
type Spacing struct {
	PanelX, Gap int
}

type Borders struct {
	Panel, Dialog lipgloss.Border
}

type Typography struct {
	TitleBold, HeadingBold, KeyBold bool
}

type Focus struct {
	Text, Fill, Border lipgloss.Color
	Marker             string
}

type Status struct {
	Text, Fill    lipgloss.Color
	Symbol, Label string
	Message       string
}

type States struct {
	Normal, Disabled, Busy, Success, Error, Empty Status
}

type Progress struct {
	Filled, Track string
}

// Theme has no maps, slices, renderer, or shared mutable styles. Start with a
// named theme and change a copy to customize a view.
type Theme struct {
	Name          string
	Palette       Palette
	Spacing       Spacing
	Borders       Borders
	Typography    Typography
	Focus         Focus
	States        States
	Progress      Progress
	ReducedMotion bool
}

// ASCIIBorder returns standard 7-bit ASCII borders (+, -, |) for modest terminals.
func ASCIIBorder() lipgloss.Border {
	return lipgloss.Border{
		Top: "-", Bottom: "-", Left: "|", Right: "|",
		TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
	}
}

// Nostalgia is the default warm cream, amber, and slate palette.
func Nostalgia() Theme {
	p := Palette{
		Canvas: "#E8DDC7", Surface: "#FFF4DC", Text: "#35434D",
		Muted: "#746957", Accent: "#8C510D", Border: "#84745E",
		Header: "#E7B65A", HeaderText: "#35434D",
	}
	return makeTheme("nostalgia", p, "#426640", "#A33632")
}

// Midnight is an explicitly selected slate theme, not an adaptive color.
func Midnight() Theme {
	p := Palette{
		Canvas: "#202B33", Surface: "#2D3942", Text: "#FFF4DC",
		Muted: "#C0B6A3", Accent: "#F2BE63", Border: "#9B8C75",
		Header: "#475664", HeaderText: "#FFF4DC",
	}
	return makeTheme("midnight", p, "#ACCE92", "#FFABA0")
}

// Monochrome provides a limited-color / monochrome theme using plain ASCII borders,
// high-contrast black/white palette, and text badges ([OK], [FAIL], [BUSY]).
func Monochrome() Theme {
	p := Palette{
		Canvas: "0", Surface: "0", Text: "15",
		Muted: "7", Accent: "15", Border: "7",
		Header: "7", HeaderText: "0",
	}
	asciiBorder := ASCIIBorder()
	return Theme{
		Name:    "monochrome",
		Palette: p,
		Spacing: Spacing{PanelX: 1, Gap: 1},
		Borders: Borders{Panel: asciiBorder, Dialog: asciiBorder},
		Typography: Typography{
			TitleBold:   true,
			HeadingBold: true,
			KeyBold:     true,
		},
		Focus: Focus{
			Text:   "0",
			Fill:   "15",
			Border: "15",
			Marker: ">",
		},
		States: States{
			Normal:   Status{Text: "15", Fill: "0", Symbol: "[READY]", Label: "READY", Message: "Ready when you are."},
			Disabled: Status{Text: "7", Fill: "0", Symbol: "[DISABLED]", Label: "DISABLED", Message: "This action is unavailable."},
			Busy:     Status{Text: "15", Fill: "0", Symbol: "[BUSY]", Label: "BUSY", Message: "Working on your request…"},
			Success:  Status{Text: "15", Fill: "0", Symbol: "[OK]", Label: "OK", Message: "All set."},
			Error:    Status{Text: "15", Fill: "0", Symbol: "[FAIL]", Label: "FAIL", Message: "Something went wrong. Try again."},
			Empty:    Status{Text: "7", Fill: "0", Symbol: "[EMPTY]", Label: "EMPTY", Message: "Nothing here yet."},
		},
		Progress:      Progress{Filled: "#", Track: "-"},
		ReducedMotion: true,
	}
}

// HighContrast provides high-contrast tokens with distinct state glyphs and text cues.
func HighContrast() Theme {
	p := Palette{
		Canvas: "#000000", Surface: "#000000", Text: "#FFFFFF",
		Muted: "#CCCCCC", Accent: "#FFFF00", Border: "#FFFFFF",
		Header: "#FFFFFF", HeaderText: "#000000",
	}
	return Theme{
		Name:    "high-contrast",
		Palette: p,
		Spacing: Spacing{PanelX: 1, Gap: 1},
		Borders: Borders{Panel: lipgloss.ThickBorder(), Dialog: lipgloss.DoubleBorder()},
		Typography: Typography{
			TitleBold:   true,
			HeadingBold: true,
			KeyBold:     true,
		},
		Focus: Focus{
			Text:   "#000000",
			Fill:   "#FFFF00",
			Border: "#FFFF00",
			Marker: "»",
		},
		States: States{
			Normal:   Status{Text: "#FFFFFF", Fill: "#000000", Symbol: "[READY]", Label: "READY", Message: "Ready when you are."},
			Disabled: Status{Text: "#888888", Fill: "#000000", Symbol: "[DISABLED]", Label: "DISABLED", Message: "This action is unavailable."},
			Busy:     Status{Text: "#FFFF00", Fill: "#000000", Symbol: "[BUSY]", Label: "BUSY", Message: "Working on your request…"},
			Success:  Status{Text: "#00FF00", Fill: "#000000", Symbol: "[OK]", Label: "OK", Message: "All set."},
			Error:    Status{Text: "#FF5555", Fill: "#000000", Symbol: "[FAIL]", Label: "FAIL", Message: "Something went wrong. Try again."},
			Empty:    Status{Text: "#CCCCCC", Fill: "#000000", Symbol: "[EMPTY]", Label: "EMPTY", Message: "Nothing here yet."},
		},
		Progress:      Progress{Filled: "━", Track: "─"},
		ReducedMotion: true,
	}
}

// HighContrastLight provides a high-contrast light theme variant.
func HighContrastLight() Theme {
	p := Palette{
		Canvas: "#FFFFFF", Surface: "#FFFFFF", Text: "#000000",
		Muted: "#444444", Accent: "#000080", Border: "#000000",
		Header: "#000000", HeaderText: "#FFFFFF",
	}
	return Theme{
		Name:    "high-contrast-light",
		Palette: p,
		Spacing: Spacing{PanelX: 1, Gap: 1},
		Borders: Borders{Panel: lipgloss.ThickBorder(), Dialog: lipgloss.DoubleBorder()},
		Typography: Typography{
			TitleBold:   true,
			HeadingBold: true,
			KeyBold:     true,
		},
		Focus: Focus{
			Text:   "#FFFFFF",
			Fill:   "#000080",
			Border: "#000080",
			Marker: "»",
		},
		States: States{
			Normal:   Status{Text: "#000000", Fill: "#FFFFFF", Symbol: "[READY]", Label: "READY", Message: "Ready when you are."},
			Disabled: Status{Text: "#666666", Fill: "#FFFFFF", Symbol: "[DISABLED]", Label: "DISABLED", Message: "This action is unavailable."},
			Busy:     Status{Text: "#804000", Fill: "#FFFFFF", Symbol: "[BUSY]", Label: "BUSY", Message: "Working on your request…"},
			Success:  Status{Text: "#006600", Fill: "#FFFFFF", Symbol: "[OK]", Label: "OK", Message: "All set."},
			Error:    Status{Text: "#AA0000", Fill: "#FFFFFF", Symbol: "[FAIL]", Label: "FAIL", Message: "Something went wrong. Try again."},
			Empty:    Status{Text: "#444444", Fill: "#FFFFFF", Symbol: "[EMPTY]", Label: "EMPTY", Message: "Nothing here yet."},
		},
		Progress:      Progress{Filled: "━", Track: "─"},
		ReducedMotion: true,
	}
}

// WithReducedMotion returns a copy of the theme with reduced motion configured.
func (t Theme) WithReducedMotion(enabled bool) Theme {
	t.ReducedMotion = enabled
	return t
}

func makeTheme(name string, p Palette, success, failure lipgloss.Color) Theme {
	return Theme{
		Name: name, Palette: p,
		Spacing:    Spacing{PanelX: 1, Gap: 1},
		Borders:    Borders{Panel: lipgloss.ThickBorder(), Dialog: lipgloss.DoubleBorder()},
		Typography: Typography{TitleBold: true, HeadingBold: true, KeyBold: true},
		Focus:      Focus{Text: p.Surface, Fill: p.Text, Border: p.Accent, Marker: "›"},
		States: States{
			Normal:   Status{Text: p.Text, Fill: p.Surface, Symbol: "○", Label: "READY", Message: "Ready when you are."},
			Disabled: Status{Text: p.Muted, Fill: p.Canvas, Symbol: "–", Label: "DISABLED", Message: "This action is unavailable."},
			Busy:     Status{Text: p.Accent, Fill: p.Surface, Symbol: "…", Label: "WORKING", Message: "Working on your request…"},
			Success:  Status{Text: success, Fill: p.Surface, Symbol: "✓", Label: "DONE", Message: "All set."},
			Error:    Status{Text: failure, Fill: p.Surface, Symbol: "!", Label: "ERROR", Message: "Something went wrong. Try again."},
			Empty:    Status{Text: p.Muted, Fill: p.Surface, Symbol: "◇", Label: "EMPTY", Message: "Nothing here yet."},
		},
		Progress: Progress{Filled: "━", Track: "·"},
	}
}

// Status returns normal tokens for an unknown state.
func (t Theme) Status(state State) Status {
	switch state {
	case Disabled:
		return t.States.Disabled
	case Busy:
		return t.States.Busy
	case Success:
		return t.States.Success
	case Error:
		return t.States.Error
	case Empty:
		return t.States.Empty
	default:
		return t.States.Normal
	}
}
