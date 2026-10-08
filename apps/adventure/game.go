// game.go is the game proper: a small state machine (room, inventory,
// flags, item locations, turn count, end state) plus the verb+noun parser.
// Nothing here knows about the SDK, IPC, or persistence — command() takes
// a raw line and returns transcript lines, which keeps the whole game
// testable as plain Go.
package adventure

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// game is one expedition's mutable state. loc maps item id to the room
// holding it; an item absent from loc is carried (present in inv).
type game struct {
	room  string
	inv   map[string]bool
	flags map[string]bool
	loc   map[string]string
	turns int
	ended string // "", "dead", or "won"
}

// newGame builds the initial state from the world tables.
func newGame() *game {
	g := &game{
		room:  startRoom,
		inv:   map[string]bool{},
		flags: map[string]bool{},
		loc:   map[string]string{},
	}
	for roomID, r := range rooms {
		for _, it := range r.Items {
			g.loc[it] = roomID
		}
	}
	return g
}

// reset returns the game to its opening state. The caller clears the
// transcript when command reports reset.
func (g *game) reset() {
	fresh := newGame()
	*g = *fresh
}

// introLines is the opening narration; also what restart rewinds to.
func introLines() []string {
	return []string{
		"— THE BRASS ELEPHANT —",
		"You are not a thief. The elephant was yours before the museum took it; " +
			"tonight you're only taking it back.",
		"(Type 'help' for the words the game understands.)",
	}
}

// command runs one line of player input and returns the transcript lines
// it produces. reset is true when the command restarted the game, telling
// the caller to clear the transcript before appending. Every outcome —
// including parser failures and unknown verbs — is an in-fiction reply,
// never an error.
func (g *game) command(input string) (lines []string, reset bool) {
	words := tokenize(input)
	if len(words) == 0 {
		return []string{`Say again? (try "help")`}, false
	}

	// Bare direction words are shorthand for "go <direction>".
	verb, ok := verbs[words[0]]
	isDir := false
	if _, ok = directions[words[0]]; ok {
		verb, isDir = "go", true
	}
	if verb == "" {
		return []string{fmt.Sprintf("You can't %q — try 'help' for the words the game knows.", words[0])}, false
	}

	rest := words[1:]
	if isDir {
		rest = append([]string{words[0]}, rest...)
	}
	// "pick up lamp" collapses to "take lamp".
	if words[0] == "pick" && len(rest) > 0 && rest[0] == "up" {
		rest = rest[1:]
	}
	rest = stripFillers(rest)
	var noun string
	switch {
	case len(rest) > 1:
		return []string{`Keep it to a verb and a thing — "take lamp", not "take the old brass lamp".`}, false
	case len(rest) == 1:
		noun = rest[0]
	}

	// After an ending the only live verbs are restart and help.
	if g.ended != "" && verb != "restart" && verb != "help" {
		return []string{`The expedition is over — "restart" begins a new one.`}, false
	}
	g.turns++

	switch verb {
	case "go":
		return g.cmdGo(noun), false
	case "take":
		return g.cmdTake(noun), false
	case "drop":
		return g.cmdDrop(noun), false
	case "look":
		return g.cmdLook(noun), false
	case "use":
		return g.cmdUse(noun), false
	case "inventory":
		return g.cmdInventory(), false
	case "help":
		return helpLines(), false
	case "restart":
		g.reset()
		lines := append(introLines(), "(The expedition begins again.)")
		return append(lines, g.lookLines()...), true
	}
	return []string{"You can't do that — try 'help'."}, false
}

// tokenize lowercases and splits on anything that isn't a letter, digit,
// or apostrophe: punctuation and shouting both parse.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
}

// stripFillers drops grammatical filler words from the noun slot.
func stripFillers(words []string) []string {
	out := words[:0]
	for _, w := range words {
		if fillers[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// cmdGo handles movement: direction resolution, exit lookup, gate
// conditions, then fatal/goal outcomes or arrival.
func (g *game) cmdGo(noun string) []string {
	if noun == "" {
		return []string{"Go where? (north, south, east, west, up, down)"}
	}
	dir, ok := directions[noun]
	if !ok {
		return []string{fmt.Sprintf("%q isn't a direction. Try north, south, east, west, up, or down.", noun)}
	}
	ex, ok := rooms[g.room].Exits[dir]
	if !ok {
		return []string{"There's no way that direction."}
	}
	if g.gateFails(ex) {
		if ex.Fatal {
			g.ended = "dead"
			return []string{ex.Fail, "The expedition ends here. Type 'restart' to try again."}
		}
		return []string{ex.Fail}
	}
	if ex.Goal {
		g.ended = "won"
		return []string{
			"You slip out the front door and into the night, the Brass Elephant " +
				"a warm weight in your pocket.",
			"*** You made it — the elephant is yours again. ***",
			"Type 'restart' to run it back.",
		}
	}
	g.room = ex.To
	return g.lookLines()
}

// gateFails reports whether an exit's declarative conditions block passage.
func (g *game) gateFails(ex exit) bool {
	if ex.If != "" && !g.flags[ex.If] {
		return true
	}
	if ex.Carry != "" && !g.inv[ex.Carry] {
		return true
	}
	return false
}

// cmdTake moves a visible, portable item into the inventory.
func (g *game) cmdTake(noun string) []string {
	if noun == "" {
		return []string{"Take what?"}
	}
	if id := g.findNoun(noun); id != "" {
		if g.inv[id] {
			return []string{fmt.Sprintf("You're already carrying the %s.", items[id].Name)}
		}
		it := items[id]
		if !it.Take {
			if it.TakeFail != "" {
				return []string{it.TakeFail}
			}
			return []string{fmt.Sprintf("The %s isn't something you can carry.", it.Name)}
		}
		g.inv[id] = true
		delete(g.loc, id)
		return []string{fmt.Sprintf("You take the %s.", it.Name)}
	}
	return []string{"You don't see that here."}
}

// cmdDrop sets a carried item down in the current room.
func (g *game) cmdDrop(noun string) []string {
	if noun == "" {
		return []string{"Drop what?"}
	}
	for id := range g.inv {
		if hasNoun(items[id], noun) {
			g.loc[id] = g.room
			delete(g.inv, id)
			return []string{fmt.Sprintf("You set down the %s.", items[id].Name)}
		}
	}
	return []string{"You aren't carrying that."}
}

// cmdLook describes the room, or one visible item when a noun is given.
func (g *game) cmdLook(noun string) []string {
	if noun == "" {
		return g.lookLines()
	}
	if id := g.findNoun(noun); id != "" {
		return []string{items[id].Desc}
	}
	return []string{"You don't see that here."}
}

// cmdUse applies an item's useRule when its conditions are met.
func (g *game) cmdUse(noun string) []string {
	if noun == "" {
		return []string{"Use what?"}
	}
	id := g.findNoun(noun)
	if id == "" {
		return []string{"You don't see that here."}
	}
	rule := items[id].Use
	if rule == nil {
		return []string{fmt.Sprintf("Nothing happens when you use the %s.", items[id].Name)}
	}
	fail := rule.Fail
	if fail == "" {
		fail = "That doesn't work here."
	}
	if rule.Room != "" && g.room != rule.Room {
		return []string{fail}
	}
	if rule.If != "" && !g.flags[rule.If] {
		return []string{fail}
	}
	if rule.Carry != "" && !g.inv[rule.Carry] {
		return []string{fail}
	}
	if rule.NotIf != "" && g.flags[rule.NotIf] {
		if rule.Done != "" {
			return []string{rule.Done}
		}
		return []string{"That's already done."}
	}
	if rule.Set != "" {
		g.flags[rule.Set] = true
	}
	return []string{rule.Text}
}

// cmdInventory lists what the player carries.
func (g *game) cmdInventory() []string {
	if len(g.inv) == 0 {
		return []string{"You are empty-handed."}
	}
	names := make([]string, 0, len(g.inv))
	for id := range g.inv {
		names = append(names, items[id].Name)
	}
	sort.Strings(names)
	return []string{"You are carrying: " + strings.Join(names, ", ") + "."}
}

// lookLines renders the current room: heading, description, visible
// things, then the exit list in canonical order.
func (g *game) lookLines() []string {
	r := rooms[g.room]
	lines := []string{"== " + r.Name + " ==", r.Desc}
	var seen []string
	for id, where := range g.loc {
		if where == g.room {
			seen = append(seen, items[id].Name)
		}
	}
	if len(seen) > 0 {
		sort.Strings(seen)
		lines = append(lines, "You see: "+strings.Join(seen, ", ")+".")
	}
	var dirs []string
	for dir := range r.Exits {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	lines = append(lines, "Exits: "+strings.Join(dirs, ", ")+".")
	return lines
}

// findNoun resolves a noun word to a visible item: anything carried or
// sitting in the current room.
func (g *game) findNoun(noun string) string {
	for id := range g.inv {
		if hasNoun(items[id], noun) {
			return id
		}
	}
	for id, roomID := range g.loc {
		if roomID == g.room && hasNoun(items[id], noun) {
			return id
		}
	}
	return ""
}

// hasNoun reports whether noun is one of the item's parser aliases.
func hasNoun(it item, noun string) bool {
	for _, n := range it.Nouns {
		if n == noun {
			return true
		}
	}
	return false
}

// helpLines is the in-fiction manual: enough to play, honest about bounds.
func helpLines() []string {
	return []string{
		"One verb, maybe one thing. The words this game knows:",
		"  go north · take lamp · drop note · look lamp · use key",
		"  inventory (or i) lists your pockets · n s e w u d are shortcuts",
		"  restart starts over · help repeats this",
		"The goal: find the Brass Elephant and get it out the front door, west of the foyer.",
	}
}
