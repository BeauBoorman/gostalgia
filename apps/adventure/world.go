// world.go is the whole game as data: rooms, exits, items, verbs, and
// direction tables. Nothing below is code-shaped — a designer extends the
// world by editing tables, and validateWorld checks referential integrity
// so a typo is a test failure, not a runtime surprise.
package adventure

import "fmt"

// exit is one direction out of a room. Gates are declarative conditions on
// the two pieces of world state — flags and the inventory — evaluated when
// the player tries to pass. A failed gate prints Fail; a Fatal gate also
// ends the run, and a Goal gate ends it in victory.
type exit struct {
	To    string // destination room id ("" only for Goal exits)
	If    string // flag that must be set
	Carry string // item that must be in the inventory
	Fail  string // in-fiction refusal when the gate fails
	Fatal bool   // a failed attempt kills the player
	Goal  bool   // passing ends the game in victory
}

// useRule is what "use <item>" does. Conditions are checked in order:
// Room, If, Carry, then NotIf (whose failure means the work is already
// done and prints Done rather than Fail). On success Set is raised.
type useRule struct {
	Room  string // item only usable in this room ("" = anywhere visible)
	If    string // requires this flag set
	Carry string // requires this item in the inventory
	NotIf string // success requires this flag unset
	Set   string // flag raised on success
	Text  string // narration on success
	Done  string // narration when NotIf's flag is already set
	Fail  string // narration when any other condition fails
}

// item is a thing in the world. Nouns are the parser aliases; Desc answers
// "look <item>". Take=false items refuse with TakeFail (or a default line).
type item struct {
	Name     string
	Nouns    []string
	Desc     string
	Take     bool
	TakeFail string
	Use      *useRule
}

// room is a place. Items lists what starts here; exits are keyed by
// canonical direction (never aliases).
type room struct {
	Name  string
	Desc  string
	Exits map[string]exit
	Items []string
}

// startRoom is where every new expedition begins.
const startRoom = "foyer"

// directions maps every accepted direction word to its canonical form.
var directions = map[string]string{
	"north": "north", "n": "north",
	"south": "south", "s": "south",
	"east": "east", "e": "east",
	"west": "west", "w": "west",
	"up": "up", "u": "up",
	"down": "down", "d": "down",
}

// verbs maps every accepted verb word to its canonical verb. Directions
// double as bare-word shortcuts for go ("north" == "go north").
var verbs = map[string]string{
	"go": "go", "walk": "go", "move": "go",
	"take": "take", "get": "take", "grab": "take", "pick": "take",
	"look": "look", "l": "look", "examine": "look", "x": "look", "read": "look", "inspect": "look",
	"use": "use", "light": "use", "unlock": "use", "open": "use",
	"drop": "drop", "put": "drop",
	"inventory": "inventory", "i": "inventory",
	"help": "help", "hint": "help", "commands": "help",
	"restart": "restart",
}

// fillers are words stripped from the noun slot: "take the lamp",
// "look at lamp", "go to north" all parse.
var fillers = map[string]bool{
	"the": true, "a": true, "an": true, "at": true, "to": true,
}

// rooms is the map of the world: six rooms around a manor foyer.
var rooms = map[string]room{
	"foyer": {
		Name: "Dusty Foyer",
		Desc: "Moonlight leaks through cracked glass. Cobwebs drape the portraits, " +
			"and the front door gapes west over the overgrown drive. Stairs drop " +
			"into blackness below and climb into the attic.",
		Exits: map[string]exit{
			"north": {To: "kitchen"},
			"east":  {To: "library"},
			"up":    {To: "attic"},
			"down": {
				To: "cellar", If: "lamp_lit", Carry: "lamp", Fatal: true,
				Fail: "You feel for the first step in the dark, miss it, and the " +
					"stone floor at the bottom does the rest.",
			},
			"west": {
				Goal: true, Carry: "elephant",
				Fail: "Not without the Brass Elephant — it's the whole reason you're here.",
			},
		},
		Items: []string{"lamp"},
	},
	"kitchen": {
		Name: "Cold Kitchen",
		Desc: "A stove furred with rust, a table set for nobody. Someone pinned " +
			"a note under a crooked magnet.",
		Exits: map[string]exit{
			"south": {To: "foyer"},
		},
		Items: []string{"note"},
	},
	"attic": {
		Name: "Attic",
		Desc: "Rafters, dust, and a steamer trunk. Through the round window the " +
			"street below looks impossibly far away.",
		Exits: map[string]exit{
			"down": {To: "foyer"},
		},
		Items: []string{"trunk"},
	},
	"cellar": {
		Name: "Flooded Cellar",
		Desc: "Black water to the ankles. Your lamp picks out a rusted key on a " +
			"nail beside the stairs.",
		Exits: map[string]exit{
			"up": {To: "foyer"},
		},
		Items: []string{"key"},
	},
	"library": {
		Name: "Hushed Library",
		Desc: "Shelves floor to ceiling, everything foxed and soft with damp. An " +
			"oak door stands north, iron-banded, with a keyhole that has seen work.",
		Exits: map[string]exit{
			"west": {To: "foyer"},
			"north": {
				To: "study", If: "study_unlocked",
				Fail: "The study door is locked.",
			},
		},
		Items: []string{"door", "book"},
	},
	"study": {
		Name: "Curator's Study",
		Desc: "One glass case, empty velvet, and on the desk where the insurance " +
			"photos were staged: the Brass Elephant.",
		Exits: map[string]exit{
			"south": {To: "library"},
		},
		Items: []string{"elephant"},
	},
}

// items is every takeable or scenery thing in the world.
var items = map[string]item{
	"lamp": {
		Name:  "brass lamp",
		Nouns: []string{"lamp", "lantern"},
		Desc:  "A brass paraffin lamp. Someone kept the reservoir full.",
		Take:  true,
		Use: &useRule{
			Carry: "lamp", NotIf: "lamp_lit", Set: "lamp_lit",
			Text: "You thumb the wheel; the lamp sputters to life.",
			Done: "It's already burning.",
			Fail: "You need to be holding it first.",
		},
	},
	"key": {
		Name:  "rusted key",
		Nouns: []string{"key"},
		Desc:  "Heavy, pitted with rust, but the teeth look true.",
		Take:  true,
		Use: &useRule{
			Room: "library", NotIf: "study_unlocked", Set: "study_unlocked",
			Text: "The key turns with a squeal — the study door clicks open.",
			Done: "The study door is already open.",
			Fail: "Nothing here takes this key.",
		},
	},
	"note": {
		Name:  "creased note",
		Nouns: []string{"note", "paper", "magnet"},
		Desc:  `"Bring light below. The key remembers the way."`,
		Take:  true,
		Use:   &useRule{Text: `It still reads: "Bring light below. The key remembers the way."`},
	},
	"door": {
		Name:     "study door",
		Nouns:    []string{"door"},
		Desc:     "Oak and iron, newer than the rest of the house. Locked, by the look of the keyhole.",
		TakeFail: "The door is part of the house.",
		Use: &useRule{
			Carry: "key", NotIf: "study_unlocked", Set: "study_unlocked",
			Text: "The rusted key fits. The door swings open.",
			Done: "The door stands open already.",
			Fail: "Locked tight — something must open it.",
		},
	},
	"book": {
		Name:     "folio",
		Nouns:    []string{"book", "books", "folio", "shelf", "shelves"},
		Desc:     "The collection catalog. One entry is underlined twice: 'BRASS ELEPHANT — on loan to the study.'",
		TakeFail: "The folio is chained to the shelf.",
		Use:      &useRule{Text: "The catalog confirms it: the Brass Elephant sits in the study, north."},
	},
	"trunk": {
		Name:     "steamer trunk",
		Nouns:    []string{"trunk", "chest"},
		Desc:     "Empty, save for moth wings and the smell of cedar.",
		TakeFail: "It's empty, and far too heavy besides.",
		Use:      &useRule{Text: "Nothing inside but dust and cedar."},
	},
	"elephant": {
		Name:  "Brass Elephant",
		Nouns: []string{"elephant", "statuette", "brass"},
		Desc:  "Small enough to pocket, heavy enough to matter. It's why you came.",
		Take:  true,
		Use:   &useRule{Text: "It gleams in the lamplight. Time to go."},
	},
}

// validateWorld checks every cross-reference in the tables: exits land on
// real rooms, gates and uses name real items and rooms, and no item is
// stranded without nouns or a description. Run from tests and Init.
func validateWorld() error {
	for id, r := range rooms {
		if r.Name == "" || r.Desc == "" {
			return fmt.Errorf("adventure: room %s needs a name and description", id)
		}
		for dir, ex := range r.Exits {
			if directions[dir] != dir {
				return fmt.Errorf("adventure: room %s exit %q is not a canonical direction", id, dir)
			}
			if ex.Goal {
				if ex.To != "" {
					return fmt.Errorf("adventure: room %s goal exit %q must not name a room", id, dir)
				}
			} else if _, ok := rooms[ex.To]; !ok {
				return fmt.Errorf("adventure: room %s exit %q leads to unknown room %q", id, dir, ex.To)
			}
			if ex.Carry != "" {
				if _, ok := items[ex.Carry]; !ok {
					return fmt.Errorf("adventure: room %s exit %q gates on unknown item %q", id, dir, ex.Carry)
				}
			}
			if ex.Fail == "" && (ex.If != "" || ex.Carry != "" || ex.Fatal || ex.Goal) {
				return fmt.Errorf("adventure: room %s gated exit %q needs a refusal line", id, dir)
			}
		}
		for _, it := range r.Items {
			if _, ok := items[it]; !ok {
				return fmt.Errorf("adventure: room %s starts with unknown item %q", id, it)
			}
		}
	}
	if _, ok := rooms[startRoom]; !ok {
		return fmt.Errorf("adventure: start room %q does not exist", startRoom)
	}
	for id, it := range items {
		if it.Name == "" || len(it.Nouns) == 0 || it.Desc == "" {
			return fmt.Errorf("adventure: item %s needs a name, nouns, and description", id)
		}
		if it.Use != nil {
			if it.Use.Room != "" {
				if _, ok := rooms[it.Use.Room]; !ok {
					return fmt.Errorf("adventure: item %s uses unknown room %q", id, it.Use.Room)
				}
			}
			if it.Use.Carry != "" {
				if _, ok := items[it.Use.Carry]; !ok {
					return fmt.Errorf("adventure: item %s uses unknown carried item %q", id, it.Use.Carry)
				}
			}
			if it.Use.Text == "" {
				return fmt.Errorf("adventure: item %s has a use rule with no narration", id)
			}
		}
	}
	return nil
}
