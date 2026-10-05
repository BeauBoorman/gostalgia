// Package events defines the environment's typed event bus. Every event
// is a value implementing Event; publishers attach a source; subscribers
// receive envelopes. Handlers run synchronously in the publisher's
// goroutine: delivery is ordered and deterministic, and the bus does no
// buffering. Handlers must be fast and must not panic; slow consumers
// should move work to their own goroutine.
package events

import (
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
}

func NewBus() *Bus {
	return &Bus{topics: make(map[string][]subscription)}
}

// Publish delivers ev to all current subscribers of ev.Type() and "*".
// Invalid publishes (nil event or empty source) are ignored.
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
		h(env)
	}
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
