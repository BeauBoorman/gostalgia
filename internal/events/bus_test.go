package events

import (
	"sync"
	"testing"
)

type testEvent struct{ tag string }

func (testEvent) Type() string { return "test.thing" }

func TestPublishDeliversToTopicAndWildcard(t *testing.T) {
	bus := NewBus()
	var mu sync.Mutex
	var topicGot, wildGot []Envelope

	cancelWild := bus.Subscribe("*", func(e Envelope) {
		mu.Lock()
		wildGot = append(wildGot, e)
		mu.Unlock()
	})
	bus.Subscribe("test.thing", func(e Envelope) {
		mu.Lock()
		topicGot = append(topicGot, e)
		mu.Unlock()
	})

	bus.Publish("tester", testEvent{tag: "one"})

	mu.Lock()
	if len(topicGot) != 1 || len(wildGot) != 1 {
		t.Fatalf("topic=%d wildcard=%d deliveries, want 1 and 1", len(topicGot), len(wildGot))
	}
	if got := topicGot[0].Payload.(testEvent).tag; got != "one" {
		t.Errorf("payload tag = %q, want %q", got, "one")
	}
	if topicGot[0].Source != "tester" {
		t.Errorf("source = %q, want tester", topicGot[0].Source)
	}
	if topicGot[0].Type != "test.thing" {
		t.Errorf("type = %q, want test.thing", topicGot[0].Type)
	}
	if topicGot[0].ID != 1 {
		t.Errorf("first envelope id = %d, want 1", topicGot[0].ID)
	}
	mu.Unlock()

	cancelWild()
	bus.Publish("tester", testEvent{tag: "two"})

	mu.Lock()
	defer mu.Unlock()
	if len(wildGot) != 1 {
		t.Errorf("wildcard got %d deliveries after cancel, want 1", len(wildGot))
	}
	if len(topicGot) != 2 {
		t.Errorf("topic got %d deliveries, want 2", len(topicGot))
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	bus := NewBus()
	n := 0
	cancel := bus.Subscribe("test.thing", func(Envelope) { n++ })
	cancel()
	bus.Publish("tester", testEvent{})
	if n != 0 {
		t.Fatalf("handler ran %d times after cancel, want 0", n)
	}
}

func TestDeliveryOrderFollowsSubscription(t *testing.T) {
	bus := NewBus()
	var order []string
	bus.Subscribe("test.thing", func(Envelope) { order = append(order, "first") })
	bus.Subscribe("test.thing", func(Envelope) { order = append(order, "second") })
	bus.Publish("tester", testEvent{})
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("delivery order = %v, want [first second]", order)
	}
}

func TestConcurrentPublish(t *testing.T) {
	bus := NewBus()
	var mu sync.Mutex
	seen := 0
	done := make(chan struct{})
	bus.Subscribe("test.thing", func(Envelope) {
		mu.Lock()
		seen++
		if seen == 100 {
			close(done)
		}
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				bus.Publish("tester", testEvent{})
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if seen != 100 {
		t.Fatalf("delivered %d of 100 events", seen)
	}
}

func TestInvalidPublishIgnored(t *testing.T) {
	bus := NewBus()
	n := 0
	bus.Subscribe("*", func(Envelope) { n++ })
	bus.Publish("", testEvent{})
	bus.Publish("tester", nil)
	if n != 0 {
		t.Fatalf("handler ran %d times for invalid publishes, want 0", n)
	}
}

func TestTopics(t *testing.T) {
	bus := NewBus()
	bus.Subscribe("a.b", func(Envelope) {})
	bus.Subscribe("*", func(Envelope) {})
	topics := bus.Topics()
	if len(topics) != 2 || topics[0] != "*" || topics[1] != "a.b" {
		t.Fatalf("topics = %v, want [* a.b]", topics)
	}
}
