package adventure

import (
	"strings"
	"testing"
)

// run feeds one input line and returns the transcript lines produced.
func run(t *testing.T, g *game, input string) []string {
	t.Helper()
	lines, _ := g.command(input)
	if len(lines) == 0 {
		t.Fatalf("command %q produced no transcript lines", input)
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			t.Fatalf("command %q produced a blank transcript line", input)
		}
	}
	return lines
}

func lastLine(lines []string) string { return lines[len(lines)-1] }

// --- the parser ---

func TestParserEmptyAndMalformed(t *testing.T) {
	g := newGame()
	if got := run(t, g, "   "); !strings.Contains(lastLine(got), "Say again") {
		t.Errorf("empty input = %q", lastLine(got))
	}
	if got := run(t, g, "!!!"); !strings.Contains(lastLine(got), "Say again") {
		t.Errorf("punctuation-only input = %q", lastLine(got))
	}
	if got := run(t, g, "take the old brass lamp"); !strings.Contains(lastLine(got), "verb and a thing") {
		t.Errorf("overlong input = %q", lastLine(got))
	}
	// Turns don't tick for input the parser can't even start on.
	if g.turns != 0 {
		t.Errorf("turns = %d after malformed input, want 0", g.turns)
	}
}

func TestParserUnknownVerbAndNoun(t *testing.T) {
	g := newGame()
	if got := run(t, g, "frotz"); !strings.Contains(lastLine(got), "can't") {
		t.Errorf("unknown verb = %q", lastLine(got))
	}
	run(t, g, "take lamp")
	if got := run(t, g, "take lamp"); !strings.Contains(lastLine(got), "already") {
		t.Errorf("re-take = %q", lastLine(got))
	}
	if got := run(t, g, "take telescope"); !strings.Contains(lastLine(got), "don't see") {
		t.Errorf("absent noun = %q", lastLine(got))
	}
	if got := run(t, g, "look telescope"); !strings.Contains(lastLine(got), "don't see") {
		t.Errorf("absent look = %q", lastLine(got))
	}
}

func TestParserMissingNouns(t *testing.T) {
	g := newGame()
	for input, want := range map[string]string{
		"take": "Take what?",
		"drop": "Drop what?",
		"use":  "Use what?",
		"go":   "Go where?",
	} {
		if got := run(t, g, input); !strings.Contains(lastLine(got), want) {
			t.Errorf("%q = %q, want %q", input, lastLine(got), want)
		}
	}
}

func TestParserAliasesCaseAndFillers(t *testing.T) {
	g := newGame()
	run(t, g, "TAKE THE LAMP!")
	if !g.inv["lamp"] {
		t.Fatal("TAKE THE LAMP! should take the lamp")
	}
	if got := run(t, g, "x lamp"); !strings.Contains(lastLine(got), "brass") {
		t.Errorf("x alias = %q", lastLine(got))
	}
	if got := run(t, g, "i"); !strings.Contains(lastLine(got), "brass lamp") {
		t.Errorf("i alias = %q", lastLine(got))
	}
	run(t, g, "go north")
	if g.room != "kitchen" {
		t.Fatalf("go north -> %q", g.room)
	}
	run(t, g, "s") // bare direction back
	if g.room != "foyer" {
		t.Fatalf("s -> %q", g.room)
	}
	run(t, g, "pick up note") // not in this room: exercises the alias honestly
	if got := run(t, g, "walk east"); g.room != "library" {
		t.Fatalf("walk east -> %q, got %v", got, g.room)
	}
}

func TestParserDirectionWords(t *testing.T) {
	g := newGame()
	if got := run(t, g, "go sideways"); !strings.Contains(lastLine(got), "isn't a direction") {
		t.Errorf("bogus direction = %q", lastLine(got))
	}
	if got := run(t, g, "go south"); !strings.Contains(lastLine(got), "no way") {
		t.Errorf("walled direction = %q", lastLine(got))
	}
}

// --- world state transitions ---

func TestTakeDropInventory(t *testing.T) {
	g := newGame()
	run(t, g, "take lamp")
	if !g.inv["lamp"] || g.loc["lamp"] != "" {
		t.Fatal("lamp should be carried after take")
	}
	run(t, g, "drop lamp")
	if g.inv["lamp"] || g.loc["lamp"] != "foyer" {
		t.Fatal("lamp should rest in the foyer after drop")
	}
	if got := run(t, g, "look"); !containsLine(got, "brass lamp") {
		t.Errorf("dropped lamp not visible in room look: %v", got)
	}
	if got := run(t, g, "inventory"); !strings.Contains(lastLine(got), "empty-handed") {
		t.Errorf("empty inventory = %q", lastLine(got))
	}
}

func TestFixedItemsRefuseTake(t *testing.T) {
	g := newGame()
	run(t, g, "go east")
	if got := run(t, g, "take door"); !strings.Contains(lastLine(got), "part of the house") {
		t.Errorf("take door = %q", lastLine(got))
	}
	if got := run(t, g, "take book"); !strings.Contains(lastLine(got), "chained") {
		t.Errorf("take book = %q", lastLine(got))
	}
}

func TestDarkCellarIsFatal(t *testing.T) {
	g := newGame()
	got := run(t, g, "go down")
	if g.ended != endedDead {
		t.Fatalf("dark descent should kill, ended = %q", g.ended)
	}
	if !containsLine(got, "restart") {
		t.Errorf("death lines should offer restart: %v", got)
	}
	// After the end, only restart and help stay live.
	if got := run(t, g, "look"); !strings.Contains(lastLine(got), "over") {
		t.Errorf("post-death look = %q", lastLine(got))
	}
	if got := run(t, g, "help"); !containsLine(got, "verb") {
		t.Errorf("post-death help = %v", got)
	}
	_, reset := g.command("restart")
	if !reset || g.ended != "" || g.room != startRoom {
		t.Error("post-death restart should produce a fresh expedition")
	}
}

func TestLampGateAndCellarKey(t *testing.T) {
	g := newGame()
	if got := run(t, g, "use lamp"); !strings.Contains(lastLine(got), "holding it") {
		t.Errorf("use lamp from the floor = %q", lastLine(got))
	}
	run(t, g, "take lamp")
	run(t, g, "use lamp")
	if !g.flags["lamp_lit"] {
		t.Fatal("use lamp should raise lamp_lit")
	}
	if got := run(t, g, "use lamp"); !strings.Contains(lastLine(got), "already") {
		t.Errorf("re-light = %q", lastLine(got))
	}
	run(t, g, "go down")
	if g.room != "cellar" || g.ended != "" {
		t.Fatalf("lit descent -> room %q ended %q", g.room, g.ended)
	}
	run(t, g, "take key")
	run(t, g, "go up")
	if g.room != "foyer" {
		t.Fatalf("go up -> %q", g.room)
	}
}

func TestLitLampLeftBehindStillKills(t *testing.T) {
	g := newGame()
	run(t, g, "take lamp")
	run(t, g, "use lamp")
	run(t, g, "drop lamp")
	got := run(t, g, "go down")
	if g.ended != endedDead {
		t.Fatalf("descending without the lamp should kill even once lit: %v", got)
	}
}

func TestKeyUnlocksStudyBothWays(t *testing.T) {
	// Via "use key" in the library.
	g := newGame()
	run(t, g, "take lamp")
	run(t, g, "use lamp")
	run(t, g, "go down")
	run(t, g, "take key")
	run(t, g, "go up")
	run(t, g, "go east")
	if got := run(t, g, "go north"); !strings.Contains(lastLine(got), "locked") {
		t.Errorf("locked study = %q", lastLine(got))
	}
	if got := run(t, g, "use key"); !strings.Contains(lastLine(got), "clicks open") {
		t.Fatalf("use key = %q", lastLine(got))
	}
	if got := run(t, g, "use key"); !strings.Contains(lastLine(got), "already") {
		t.Errorf("re-unlock = %q", lastLine(got))
	}
	run(t, g, "go north")
	if g.room != "study" {
		t.Fatalf("go north -> %q", g.room)
	}

	// Via "use door" while carrying the key.
	g2 := newGame()
	run(t, g2, "take lamp")
	run(t, g2, "use lamp")
	run(t, g2, "go down")
	run(t, g2, "take key")
	run(t, g2, "go up")
	run(t, g2, "go east")
	if got := run(t, g2, "use door"); !strings.Contains(lastLine(got), "swings open") {
		t.Fatalf("use door = %q", lastLine(got))
	}
	if !g2.flags["study_unlocked"] {
		t.Error("use door should raise study_unlocked")
	}

	// The key does nothing away from the library.
	g3 := newGame()
	run(t, g3, "take lamp")
	run(t, g3, "use lamp")
	run(t, g3, "go down")
	run(t, g3, "take key")
	if got := run(t, g3, "use key"); !strings.Contains(lastLine(got), "Nothing here") {
		t.Errorf("use key in cellar = %q", lastLine(got))
	}
}

func TestGoalExitNeedsElephant(t *testing.T) {
	g := newGame()
	if got := run(t, g, "go west"); !strings.Contains(lastLine(got), "reason you're here") {
		t.Errorf("empty-handed exit = %q", lastLine(got))
	}
	if g.ended != "" {
		t.Errorf("a refused exit should not end the game: %q", g.ended)
	}
}

// TestWalkthroughToVictory is the completable proof: a scripted run
// through the whole puzzle chain ends in the win state.
func TestWalkthroughToVictory(t *testing.T) {
	g := newGame()
	steps := []string{
		"take lamp", "use lamp",
		"go north", "take note", "look note", "go south",
		"go down", "take key", "go up",
		"go east", "use key", "go north",
		"take elephant", "go south", "go west", "go west",
	}
	for _, step := range steps {
		got := run(t, g, step)
		if g.ended == endedDead && step != steps[len(steps)-1] {
			t.Fatalf("walkthrough died at %q: %v", step, got)
		}
	}
	if g.ended != endedWon {
		t.Fatalf("walkthrough should end in victory, ended = %q", g.ended)
	}
	if !g.inv["elephant"] {
		t.Error("victory should find the elephant carried")
	}
	// Post-victory the parser answers only restart/help.
	if got := run(t, g, "inventory"); !strings.Contains(lastLine(got), "over") {
		t.Errorf("post-victory inventory = %q", lastLine(got))
	}
}

func TestRestartRestartsClean(t *testing.T) {
	g := newGame()
	run(t, g, "take lamp")
	run(t, g, "use lamp")
	run(t, g, "go north")
	if _, reset := g.command("restart"); !reset {
		t.Fatal("restart should report a reset")
	}
	if g.room != startRoom || len(g.inv) != 0 || len(g.flags) != 0 || g.ended != "" {
		t.Errorf("restart left dirty state: %+v", g)
	}
	if g.loc["lamp"] != "foyer" {
		t.Error("lamp should be back in the foyer after restart")
	}
}

// TestWorldDataIsConsistent proves the tables a designer edits stay
// internally sound: every exit lands, every gate names a real item, every
// starting item exists.
func TestWorldDataIsConsistent(t *testing.T) {
	if err := validateWorld(); err != nil {
		t.Fatalf("world data: %v", err)
	}
	// Every room reachable from the start by ignoring gates; nothing
	// orphaned. BFS over the exit table.
	seen := map[string]bool{startRoom: true}
	queue := []string{startRoom}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		for _, ex := range rooms[r].Exits {
			if ex.To != "" && !seen[ex.To] {
				seen[ex.To] = true
				queue = append(queue, ex.To)
			}
		}
	}
	for id := range rooms {
		if !seen[id] {
			t.Errorf("room %q is unreachable from %q", id, startRoom)
		}
	}
}

func containsLine(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
