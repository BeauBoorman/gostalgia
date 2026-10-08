package petwatch

import (
	"fmt"
	"strings"
	"time"
)

// Stat bounds: every stat is a fraction of statMax. Higher is always
// better — hunger means fullness, so 0 hunger is starving.
const statMax = 100.0

// Decay and recovery rates per hour of elapsed wall-clock time. The pet
// ages by real elapsed time, not app uptime: the same advance() call runs
// over the gap between persisted timestamps on load and over the live
// tick while the app runs.
const (
	hungerDecayPerHour    = 5.0  // full -> starving in ~20 hours
	happinessDecayPerHour = 4.0  // joyful -> miserable in ~25 hours
	energyDecayPerHour    = 2.5  // awake drain; ~40 hours to collapse
	energyRegenPerHour    = 25.0 // asleep recovery; ~4 hours to rested
	asleepDecayFactor     = 0.5  // sleep halves hunger/happiness decay
)

// Lifecycle thresholds and window boundaries.
const (
	hatchAfter      = 1 * time.Hour  // egg -> chick
	adultAfter      = 24 * time.Hour // chick -> adult
	sleepCollapseAt = 10.0           // energy this low knocks the pet out
	sleepRestedAt   = 90.0           // a sleeping pet wakes once rested
	sickEnterAt     = 15.0           // hunger or happiness this low: sick
	sickExitAt      = 30.0           // ... and both must clear this to cure
)

// Feeding and play values.
const (
	feedHunger     = 30.0 // completing a task
	feedHappiness  = 15.0
	feedEnergy     = 5.0
	playHappiness  = 10.0
	playEnergyCost = 8.0
	addTaskHappy   = 2.0 // a fresh promise of future food
)

// Task bounds.
const (
	maxTaskTitle  = 120
	maxTasksTotal = 50 // pending + retained completed
	maxDoneRetain = 25 // completed tasks kept for history
)

// Stage is the pet's lifecycle stage, derived from age.
type Stage string

const (
	StageEgg   Stage = "egg"
	StageChick Stage = "chick"
	StageAdult Stage = "adult"
)

// Mood is the rendered disposition, derived from stats and conditions.
type Mood string

const (
	MoodEating  Mood = "eating"
	MoodPlaying Mood = "playing"
	MoodAsleep  Mood = "asleep"
	MoodSick    Mood = "sick"
	MoodSad     Mood = "sad"
	MoodContent Mood = "content"
	MoodHappy   Mood = "happy"
)

// Pet is the persistent creature state. Stats and UpdatedAt are always
// written together so reload decays from a consistent (stats, timestamp)
// pair — unsaved advances are recomputed honestly from the stored anchor.
type Pet struct {
	Name      string    `json:"name"`
	BornAt    time.Time `json:"born_at"`
	Hunger    float64   `json:"hunger"`
	Happiness float64   `json:"happiness"`
	Energy    float64   `json:"energy"`
	Sleeping  bool      `json:"sleeping"`
	Sick      bool      `json:"sick"`
	Feeds     int       `json:"feeds"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Task is one to-do item; completing it feeds the pet.
type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"created_at"`
	DoneAt    time.Time `json:"done_at,omitempty"`
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > statMax {
		return statMax
	}
	return v
}

func newPet(now time.Time) Pet {
	return Pet{
		Name:      "Mochi",
		BornAt:    now,
		Hunger:    80,
		Happiness: 80,
		Energy:    90,
		UpdatedAt: now,
	}
}

// stage returns the lifecycle stage at the given instant.
func (p *Pet) stage(now time.Time) Stage {
	age := now.Sub(p.BornAt)
	switch {
	case age < hatchAfter:
		return StageEgg
	case age < adultAfter:
		return StageChick
	default:
		return StageAdult
	}
}

// mood reports the rendered disposition at the given instant. An egg has
// no mood; flashes (eating/playing) override everything so feeding is
// visibly immediate; the conditions rank below that.
func (p *Pet) mood(now time.Time) Mood {
	if p.Sleeping && p.stage(now) != StageEgg {
		return MoodAsleep
	}
	if p.Sick {
		return MoodSick
	}
	if p.Hunger <= sickEnterAt || p.Happiness <= sickEnterAt {
		return MoodSad
	}
	if p.Hunger >= 60 && p.Happiness >= 60 {
		return MoodHappy
	}
	return MoodContent
}

// advance applies wall-clock decay and recovery since the last update.
// Backwards clock movement only re-anchors the timestamp; stats never
// rewind. Sleep halves needs decay, restores energy, collapses at
// sleepCollapseAt while awake and auto-wakes at sleepRestedAt.
func (p *Pet) advance(now time.Time) {
	if !now.After(p.UpdatedAt) {
		p.UpdatedAt = now
		return
	}
	hours := now.Sub(p.UpdatedAt).Hours()
	p.UpdatedAt = now
	rate := 1.0
	if p.Sleeping {
		rate = asleepDecayFactor
	}
	p.Hunger = clamp(p.Hunger - hungerDecayPerHour*hours*rate)
	p.Happiness = clamp(p.Happiness - happinessDecayPerHour*hours*rate)
	if p.Sleeping {
		p.Energy = clamp(p.Energy + energyRegenPerHour*hours)
		if p.Energy >= sleepRestedAt {
			p.Sleeping = false
		}
	} else {
		p.Energy = clamp(p.Energy - energyDecayPerHour*hours)
		if p.Energy <= sleepCollapseAt {
			p.Sleeping = true
		}
	}
	// Sickness sticks to hatched pets and clears only when both needs
	// recover past the exit threshold — a snack does not cure misery.
	if p.stage(now) == StageEgg {
		p.Sick = false
	} else if !p.Sick && (p.Hunger <= sickEnterAt || p.Happiness <= sickEnterAt) {
		p.Sick = true
	} else if p.Sick && p.Hunger > sickExitAt && p.Happiness > sickExitAt {
		p.Sick = false
	}
}

// feed applies the nourishment of a completed task.
func (p *Pet) feed() {
	p.Hunger = clamp(p.Hunger + feedHunger)
	p.Happiness = clamp(p.Happiness + feedHappiness)
	p.Energy = clamp(p.Energy + feedEnergy)
	p.Feeds++
	if p.Sick && p.Hunger > sickExitAt && p.Happiness > sickExitAt {
		p.Sick = false
	}
}

// play is the awake, hatched-only interaction. It costs energy.
func (p *Pet) play(now time.Time) error {
	switch {
	case p.stage(now) == StageEgg:
		return fmt.Errorf("%s is still an egg", p.Name)
	case p.Sleeping:
		return fmt.Errorf("%s is asleep", p.Name)
	case p.Energy <= playEnergyCost:
		return fmt.Errorf("%s is too tired to play", p.Name)
	}
	p.Happiness = clamp(p.Happiness + playHappiness)
	p.Energy = clamp(p.Energy - playEnergyCost)
	return nil
}

// toggleSleep puts a hatched pet down for a nap or wakes a willing one.
// A pet at or below the collapse threshold cannot be woken — it is out
// cold until it has rested.
func (p *Pet) toggleSleep(now time.Time) error {
	if p.stage(now) == StageEgg {
		return fmt.Errorf("eggs do not nap")
	}
	if !p.Sleeping {
		p.Sleeping = true
		return nil
	}
	if p.Energy <= sleepCollapseAt {
		return fmt.Errorf("%s is too exhausted to wake", p.Name)
	}
	p.Sleeping = false
	return nil
}

// sanitizeTaskTitle normalizes user input: trimmed, single-line, bounded.
func sanitizeTaskTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) > maxTaskTitle {
		s = strings.TrimSpace(s[:maxTaskTitle])
	}
	return s
}
