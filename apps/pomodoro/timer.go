package pomodoro

import (
	"fmt"
	"time"
)

// Phase names the timer's current segment. The flow is
// work -> short break -> work -> ... -> long break -> work, with a long
// break after every Config.Cycles completed work phases.
type Phase string

const (
	PhaseIdle  Phase = "idle"
	PhaseWork  Phase = "work"
	PhaseShort Phase = "short"
	PhaseLong  Phase = "long"
)

// phaseLabels render phases for items, meters, and status text.
var phaseLabels = map[Phase]string{
	PhaseIdle:  "Ready",
	PhaseWork:  "Work",
	PhaseShort: "Short break",
	PhaseLong:  "Long break",
}

// Config bounds: work 5-120 minutes, either break 1-30 minutes, and a
// cycle of 1-8 work sessions. Anything outside is rejected, not clamped.
const (
	minWorkMin   = 5
	maxWorkMin   = 120
	minBreakMin  = 1
	maxBreakMin  = 30
	minCycles    = 1
	maxCycles    = 8
	maxLogRetain = 50 // completed phase entries kept for history
)

// Config is the timer's phase-length declaration, in whole minutes.
// Lengths apply to phases started after the change; a running phase
// keeps the anchor it began with.
type Config struct {
	WorkMin  int `json:"work_min"`
	ShortMin int `json:"short_min"`
	LongMin  int `json:"long_min"`
	Cycles   int `json:"cycles"`
}

func defaultConfig() Config {
	return Config{WorkMin: 25, ShortMin: 5, LongMin: 15, Cycles: 4}
}

// Validate rejects out-of-bounds lengths with an actionable message.
func (c Config) Validate() error {
	if c.WorkMin < minWorkMin || c.WorkMin > maxWorkMin {
		return fmt.Errorf("work must be %d-%d minutes, got %d", minWorkMin, maxWorkMin, c.WorkMin)
	}
	if c.ShortMin < minBreakMin || c.ShortMin > maxBreakMin {
		return fmt.Errorf("short break must be %d-%d minutes, got %d", minBreakMin, maxBreakMin, c.ShortMin)
	}
	if c.LongMin < minBreakMin || c.LongMin > maxBreakMin {
		return fmt.Errorf("long break must be %d-%d minutes, got %d", minBreakMin, maxBreakMin, c.LongMin)
	}
	if c.Cycles < minCycles || c.Cycles > maxCycles {
		return fmt.Errorf("cycle length must be %d-%d sessions, got %d", minCycles, maxCycles, c.Cycles)
	}
	return nil
}

// Length is the configured duration of one phase; zero for idle.
func (c Config) Length(p Phase) time.Duration {
	switch p {
	case PhaseWork:
		return time.Duration(c.WorkMin) * time.Minute
	case PhaseShort:
		return time.Duration(c.ShortMin) * time.Minute
	case PhaseLong:
		return time.Duration(c.LongMin) * time.Minute
	}
	return 0
}

// Timer is the persisted anchor state. Exactly one of EndsAt (running)
// or Remaining (paused) is meaningful: a running phase derives its
// countdown from the pinned EndsAt so the displayed remainder is always
// ends_at - now, never a decremented counter; a paused phase freezes its
// remainder and the countdown does not move. SavedAt stamps the last
// write so a relaunch can report the paused remainder honestly instead
// of pretending the timer ran while the app was down.
type Timer struct {
	Phase     Phase         `json:"phase"`
	Running   bool          `json:"running"`
	EndsAt    time.Time     `json:"ends_at,omitempty"`
	Remaining time.Duration `json:"remaining,omitempty"`
	StartedAt time.Time     `json:"started_at,omitempty"`
	SavedAt   time.Time     `json:"saved_at,omitempty"`
}

// remaining reports the countdown at the given instant.
func (t Timer) remaining(now time.Time) time.Duration {
	var r time.Duration
	if t.Phase == PhaseIdle {
		return 0
	}
	if t.Running {
		r = t.EndsAt.Sub(now)
	} else {
		r = t.Remaining
	}
	if r < 0 {
		return 0
	}
	return r
}

// LogEntry is one ended phase in the bounded session history. EndedAt is
// when the transition was observed, so a skipped phase or a phase that
// outlived a dormant stretch shows real wall-clock bounds.
type LogEntry struct {
	Phase     Phase     `json:"phase"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Skipped   bool      `json:"skipped,omitempty"`
}

// clock renders a duration as m:ss (or h:mm:ss past an hour), rounding
// up so a live countdown never reads 0:00 while time remains.
func clock(d time.Duration) string {
	if d <= 0 {
		return "0:00"
	}
	total := int((d + time.Second - time.Nanosecond) / time.Second)
	if total >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", total/3600, (total%3600)/60, total%60)
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}
