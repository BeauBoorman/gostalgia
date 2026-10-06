package theme

import "testing"

func TestNamedThemes(t *testing.T) {
	for _, th := range []Theme{Nostalgia(), Midnight()} {
		t.Run(th.Name, func(t *testing.T) {
			if th.Palette.Canvas == th.Palette.Text || th.Palette.Surface == th.Palette.Text {
				t.Fatal("text and background need distinct colors")
			}
			if th.Spacing.PanelX != 1 || th.Spacing.Gap != 1 || th.Focus.Marker == "" {
				t.Fatal("missing layout or focus tokens")
			}
			for _, state := range []State{Normal, Disabled, Busy, Success, Error, Empty} {
				s := th.Status(state)
				if s.Text == "" || s.Fill == "" || s.Symbol == "" || s.Label == "" || s.Message == "" {
					t.Errorf("incomplete %s status: %+v", state, s)
				}
			}
			if th.Status("unknown") != th.States.Normal {
				t.Fatal("unknown state must use normal tokens")
			}
		})
	}
	if Nostalgia().Palette == Midnight().Palette {
		t.Fatal("named palettes must differ")
	}
}

func TestThemeIsAnIndependentValue(t *testing.T) {
	custom := Nostalgia()
	custom.Focus.Marker = "*"
	custom.Spacing.PanelX = 0
	custom.States.Busy.Label = "PLEASE WAIT"
	if got := Nostalgia(); got.Focus.Marker != "›" || got.Spacing.PanelX != 1 || got.States.Busy.Label != "WORKING" {
		t.Fatal("customizing a copy changed the default")
	}
}
