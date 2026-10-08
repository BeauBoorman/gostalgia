package pomodoro

import (
	"strings"
	"time"
)

// digitFont is a 3x5 block font for the countdown. Rows are joined with
// single-space gaps so "24:59" renders five lines tall and fixed-width.
var digitFont = map[rune][5]string{
	'0': {"###", "#.#", "#.#", "#.#", "###"},
	'1': {"..#", "..#", "..#", "..#", "..#"},
	'2': {"###", "..#", "###", "#..", "###"},
	'3': {"###", "..#", "###", "..#", "###"},
	'4': {"#.#", "#.#", "###", "..#", "..#"},
	'5': {"###", "#..", "###", "..#", "###"},
	'6': {"###", "#..", "###", "#.#", "###"},
	'7': {"###", "..#", "..#", "..#", "..#"},
	'8': {"###", "#.#", "###", "#.#", "###"},
	'9': {"###", "#.#", "###", "..#", "###"},
	':': {"...", ".#.", "...", ".#.", "..."},
	' ': {"...", "...", "...", "...", "..."},
}

// bigClock renders the remaining time in the block font. Any glyph
// outside the font is dropped, so the block never breaks its five-line
// frame.
func bigClock(d time.Duration) string {
	var lines [5]strings.Builder
	for _, r := range clock(d) {
		glyph, ok := digitFont[r]
		if !ok {
			glyph = digitFont[' ']
		}
		for i := 0; i < 5; i++ {
			if lines[i].Len() > 0 {
				lines[i].WriteByte(' ')
			}
			lines[i].WriteString(glyph[i])
		}
	}
	out := make([]string, 5)
	for i := range lines {
		out[i] = lines[i].String()
	}
	return strings.Join(out, "\n")
}
