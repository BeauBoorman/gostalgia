package events

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestBufferedDropOldestReplayAndClose(t *testing.T) {
	bus := NewBus()
	sub, info, err := bus.SubscribeBuffered("*", 2, nil)
	if err != nil || info.Cursor != 0 {
		t.Fatalf("subscribe: %+v, %v", info, err)
	}
	for i := 0; i < 5; i++ {
		bus.Publish("tester", testEvent{})
	}
	first, last := <-sub.Events, <-sub.Events
	if first.Event.ID != 4 || last.Event.ID != 5 || last.Dropped != 3 {
		t.Fatalf("drop oldest: %+v, %+v", first, last)
	}
	after := uint64(1)
	replay, snapshot, err := bus.SubscribeBuffered("*", 2, &after)
	if err != nil || !snapshot.Resync || snapshot.Cursor != 5 {
		t.Fatalf("replay: %+v, %v", snapshot, err)
	}
	if got := <-replay.Events; got.Event.ID != 4 {
		t.Fatalf("replay starts at %+v", got)
	}
	replay.Close()
	sub.Close()
	sub.Close()
	bus.Publish("tester", testEvent{})
	if _, ok := <-sub.Events; ok {
		t.Fatal("closed subscription received a publish")
	}
	if len(bus.streams) != 0 {
		t.Fatal("subscription leaked")
	}
}

func TestHistoryRetentionFilterAndPagination(t *testing.T) {
	bus := NewBus()
	for i := 0; i < HistoryLimit+10; i++ {
		bus.Publish("tester", testEvent{})
	}
	h, err := bus.Recent("test.thing", 0, 3)
	if err != nil || h.Oldest != 11 || h.Cursor != HistoryLimit+10 ||
		!h.Resync || !h.More || len(h.Events) != 3 || h.Next != 13 {
		t.Fatalf("history: %+v, %v", h, err)
	}
	next, err := bus.Recent("*", h.Next, HistoryLimit)
	if err != nil || next.Resync || next.More || next.Next != h.Cursor || len(next.Events) != HistoryLimit-3 {
		t.Fatalf("next page: %+v, %v", next, err)
	}
	empty, err := bus.Recent("other", 0, 1)
	if err != nil || len(empty.Events) != 0 || empty.Next != h.Cursor {
		t.Fatalf("filtered: %+v, %v", empty, err)
	}
	future, _ := bus.Recent("*", h.Cursor+1, 1)
	if !future.Resync {
		t.Fatal("cursor from another runtime not reported")
	}
}

type secretEvent struct {
	Token    string
	Password string
	Document string
}

func (secretEvent) Type() string { return "test.secret" }

func TestHistoryDoesNotRetainPayloadsOrFreeFormMetadata(t *testing.T) {
	bus := NewBus()
	bus.Publish("source with credential=value", secretEvent{
		Token: "token-value", Password: "password-value", Document: "document contents",
	})
	h, _ := bus.Recent("*", 0, 0)
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"credential", "token-value", "password-value", "document contents", "Payload"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("history contains %q: %s", secret, b)
		}
	}
	if h.Events[0].Source != "redacted" {
		t.Fatal("free-form source retained")
	}
}

func TestBufferedConcurrentPublishAndCancel(t *testing.T) {
	bus := NewBus()
	sub, _, err := bus.SubscribeBuffered("*", BufferLimit, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 128; j++ {
				bus.Publish("tester", testEvent{})
				extra, _, _ := bus.SubscribeBuffered("*", 1, nil)
				extra.Close()
			}
		}()
	}
	wg.Wait()
	sub.Close()
	var previous uint64
	var last Delivery
	count := 0
	for item := range sub.Events {
		if item.Event.ID <= previous {
			t.Fatal("concurrent publish order is not monotonic")
		}
		previous, last = item.Event.ID, item
		count++
	}
	if count != BufferLimit || last.Event.ID != 1024 || last.Dropped != 1024-BufferLimit {
		t.Fatalf("bounded delivery: count=%d last=%+v", count, last)
	}
}

func TestBufferedInvalidLimits(t *testing.T) {
	bus := NewBus()
	for _, buffer := range []int{-1, BufferLimit + 1} {
		if _, _, err := bus.SubscribeBuffered("*", buffer, nil); err == nil {
			t.Fatalf("accepted buffer %d", buffer)
		}
	}
	for _, limit := range []int{-1, HistoryLimit + 1} {
		if _, err := bus.Recent("*", 0, limit); err == nil {
			t.Fatalf("accepted history limit %d", limit)
		}
	}
	if _, err := bus.Recent("invalid topic", 0, 0); err == nil {
		t.Fatal("accepted invalid topic")
	}
}

func TestBufferedSubscriptionLimitReleasesOnClose(t *testing.T) {
	bus := NewBus()
	subs := make([]*Subscription, 0, SubscriptionLimit)
	for i := 0; i < SubscriptionLimit; i++ {
		sub, _, err := bus.SubscribeBuffered("*", 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, sub)
	}
	if _, _, err := bus.SubscribeBuffered("*", 1, nil); err == nil {
		t.Fatal("subscription flood accepted")
	}
	subs[0].Close()
	replacement, _, err := bus.SubscribeBuffered("*", 1, nil)
	if err != nil {
		t.Fatal("closed subscription still counts toward limit")
	}
	replacement.Close()
	for _, sub := range subs {
		sub.Close()
	}
	if bus.SubscriberCount() != 0 {
		t.Fatal("subscriptions leaked")
	}
}
