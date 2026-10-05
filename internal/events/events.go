// Package events defines the environment's typed event bus. Every event
// is a value implementing Event; publishers attach a source; subscribers
// receive envelopes. Handlers run synchronously in the publisher's
// goroutine: delivery is ordered and deterministic, and the bus does no
// buffering. Handlers must be fast and should not panic; slow consumers
// should move work to their own goroutine. A panicking handler is
// contained by the bus and logged with its topic — one bad subscriber
// cannot take down the publisher or the runtime.
package events

import (
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Event is implemented by every event payload. Each subsystem defines its
// own event types; nothing publishes untyped maps.
type Event interface {
	Type() string
}

// Envelope wraps an event with delivery metadata.
type Envelope struct {
	ID      uint64    `json:"id"`
	Type    string    `json:"type"`
	Source  string    `json:"source"`
	Time    time.Time `json:"time"`
	Payload Event     `json:"payload"`
}

// Handler receives published envelopes. Subscribe topic "*" to receive
// every event (wildcard subscribers run before topic subscribers).
type Handler func(Envelope)

type subscription struct {
	id uint64
	h  Handler
}

// Bus is the environment event bus.
type Bus struct {
	mu     sync.RWMutex
	seq    uint64
	subSeq uint64
	topics map[string][]subscription
	log    *slog.Logger // reports subscriber panics; never nil
}

func NewBus() *Bus { return NewBusWithLogger(nil) }

// NewBusWithLogger builds a bus that reports subscriber panics to log. A
// nil logger falls back to the slog default.
func NewBusWithLogger(log *slog.Logger) *Bus {
	if log == nil {
		log = slog.Default()
	}
	return &Bus{topics: make(map[string][]subscription), log: log}
}

// Publish delivers ev to all current subscribers of ev.Type() and "*".
// Invalid publishes (nil event or empty source) are ignored. A handler
// that panics is contained here and logged; delivery to the remaining
// handlers continues.
func (b *Bus) Publish(source string, ev Event) {
	if ev == nil || source == "" {
		return
	}
	b.mu.Lock()
	b.seq++
	env := Envelope{
		ID:      b.seq,
		Type:    ev.Type(),
		Source:  source,
		Time:    time.Now(),
		Payload: ev,
	}
	targets := make([]Handler, 0, len(b.topics[env.Type])+len(b.topics["*"]))
	for _, sub := range b.topics["*"] {
		targets = append(targets, sub.h)
	}
	for _, sub := range b.topics[env.Type] {
		targets = append(targets, sub.h)
	}
	b.mu.Unlock()
	for _, h := range targets {
		deliver(b.log, env, h)
	}
}

// deliver invokes one handler, containing its panic: handlers are
// subscribers across a trust line (applications subscribe from inside the
// runtime), and a panic must not propagate into the publisher's goroutine.
func deliver(log *slog.Logger, env Envelope, h Handler) {
	defer func() {
		if p := recover(); p != nil {
			log.Error("events: subscriber panicked",
				"topic", env.Type,
				"source", env.Source,
				"event_id", env.ID,
				"panic", p,
			)
		}
	}()
	h(env)
}

// Subscribe registers h for topic and returns a cancel function that
// removes the subscription.
func (b *Bus) Subscribe(topic string, h Handler) (cancel func()) {
	if topic == "" {
		topic = "*"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subSeq++
	id := b.subSeq
	b.topics[topic] = append(b.topics[topic], subscription{id: id, h: h})
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		subs := b.topics[topic]
		for i, sub := range subs {
			if sub.id == id {
				b.topics[topic] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
	}
}

// Topics lists topics that currently have subscribers (including "*").
func (b *Bus) Topics() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.topics))
	for topic := range b.topics {
		out = append(out, topic)
	}
	sort.Strings(out)
	return out
}
