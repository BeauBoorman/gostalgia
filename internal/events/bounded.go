package events

import (
	"fmt"
	"time"
)

const (
	HistoryLimit      = 256
	BufferLimit       = 256
	DefaultBuffer     = 64
	SubscriptionLimit = 128
)

// Record is the remote-safe event/audit schema. Payloads are deliberately
// absent, including error text, credentials, document contents and RPC data.
// Type and Source must be non-sensitive identifiers, not user content.
type Record struct {
	ID     uint64    `json:"id"`
	Type   string    `json:"type"`
	Source string    `json:"source"`
	Time   time.Time `json:"time"`
}

// History is an oldest-first, filtered page from a bounded, in-memory trail.
// Cursor is the snapshot high-water mark; Next is the pagination cursor.
// Resync means After is outside retained history (or ahead of this bus).
type History struct {
	Epoch  string   `json:"epoch"`
	Events []Record `json:"events"`
	Oldest uint64   `json:"oldest"`
	Cursor uint64   `json:"cursor"`
	Next   uint64   `json:"next"`
	More   bool     `json:"more"`
	Resync bool     `json:"resync"`
}

type Delivery struct {
	Event   Record `json:"event"`
	Dropped uint64 `json:"dropped"`
}

// Subscription uses drop-oldest: publishing never waits for a consumer.
// Dropped is cumulative for this subscription, including replay overflow.
// All sends, drops and closes occur under the bus lock.
type Subscription struct {
	ID      uint64
	Events  <-chan Delivery
	bus     *Bus
	topic   string
	queue   chan Delivery
	dropped uint64
}

func (s *Subscription) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	if _, ok := s.bus.streams[s.ID]; ok {
		delete(s.bus.streams, s.ID)
		close(s.queue)
	}
}

func (s *Subscription) enqueue(record Record) {
	select {
	case s.queue <- Delivery{Event: record, Dropped: s.dropped}:
		return
	default:
	}
	select {
	case <-s.queue:
		s.dropped++
	default: // the consumer freed a slot meanwhile
	}
	s.queue <- Delivery{Event: record, Dropped: s.dropped}
}

// SubscribeBuffered atomically snapshots/replays history and attaches to
// future publishes. A nil after starts live at Cursor; a non-nil after
// replays retained matching records with IDs greater than *after.
func (b *Bus) SubscribeBuffered(topic string, buffer int, after *uint64) (*Subscription, History, error) {
	if err := validateTopic(topic); err != nil {
		return nil, History{}, err
	}
	if buffer == 0 {
		buffer = DefaultBuffer
	}
	if buffer < 1 || buffer > BufferLimit {
		return nil, History{}, fmt.Errorf("events: buffer must be 1..%d", BufferLimit)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.streams) >= SubscriptionLimit {
		return nil, History{}, fmt.Errorf("events: subscription limit reached")
	}
	b.subSeq++
	q := make(chan Delivery, buffer)
	sub := &Subscription{ID: b.subSeq, Events: q, queue: q, bus: b, topic: topic}
	snapshot := b.historyLocked(topic, b.seq, HistoryLimit)
	if after != nil {
		snapshot = b.historyLocked(topic, *after, HistoryLimit)
		for _, record := range snapshot.Events {
			sub.enqueue(record)
		}
		snapshot.Resync = snapshot.Resync || sub.dropped > 0
	}
	// The subscribe response contains cursor metadata, not a duplicate replay.
	snapshot.Events = nil
	b.streams[sub.ID] = sub
	return sub, snapshot, nil
}

func (b *Bus) Recent(topic string, after uint64, limit int) (History, error) {
	if err := validateTopic(topic); err != nil {
		return History{}, err
	}
	if limit == 0 {
		limit = HistoryLimit
	}
	if limit < 1 || limit > HistoryLimit {
		return History{}, fmt.Errorf("events: limit must be 1..%d", HistoryLimit)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.historyLocked(topic, after, limit), nil
}

// Epoch identifies this bus lifetime; cursors must not span runtime restarts.
func (b *Bus) Epoch() string { return b.epoch }

func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.streams)
}

func (b *Bus) historyLocked(topic string, after uint64, limit int) History {
	h := History{Epoch: b.epoch, Events: make([]Record, 0), Cursor: b.seq, Next: b.seq}
	if len(b.recent) > 0 {
		h.Oldest = b.recent[b.head].ID
	}
	h.Resync = after > b.seq || (h.Oldest > 0 && after < h.Oldest-1)
	for i := 0; i < len(b.recent); i++ {
		record := b.recent[(b.head+i)%len(b.recent)]
		if record.ID <= after || (topic != "*" && topic != record.Type) {
			continue
		}
		if len(h.Events) == limit {
			h.More = true
			h.Next = h.Events[len(h.Events)-1].ID
			break
		}
		h.Events = append(h.Events, record)
	}
	return h
}

func validateTopic(topic string) error {
	if topic != "*" && (topic == "" || identifier(topic) != topic) {
		return fmt.Errorf("events: topic must be '*' or an identifier of at most 128 bytes")
	}
	return nil
}

// Reject free-form or oversized metadata rather than retaining user text.
func identifier(s string) string {
	if s == "" || len(s) > 128 {
		return "redacted"
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '-' || c == '/') {
			return "redacted"
		}
	}
	return s
}
