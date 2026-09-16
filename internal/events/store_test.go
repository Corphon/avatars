// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package events

import (
	"testing"
	"time"
)

func TestStore_AppendsHistoryAndBroadcasts(t *testing.T) {
	store := NewStore()
	stream, unsubscribe := store.Subscribe()
	defer unsubscribe()

	event := Envelope{EventID: "evt-1", Sequence: 1, Type: "run.started", EmittedAt: time.Now().UTC(), Payload: map[string]any{"input": "demo"}}
	store.Append(event)

	history := store.History()
	if len(history) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(history))
	}

	select {
	case received := <-stream:
		if received.EventID != event.EventID {
			t.Fatalf("expected event id %s, got %s", event.EventID, received.EventID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected broadcast event")
	}
}

func TestF72_StoreKeepsNewestWhenSubscriberBackpressured(t *testing.T) {
	store := NewStore()
	stream, unsubscribe := store.Subscribe()
	defer unsubscribe()

	// Fill the subscriber buffer without draining.
	for i := 0; i < subscriberBufferSize+8; i++ {
		store.Append(Envelope{
			EventID:   "evt",
			Sequence:  uint64(i + 1),
			Type:      "llm.started",
			EmittedAt: time.Now().UTC(),
			Payload:   map[string]any{"i": i},
		})
	}

	var last Envelope
	drained := 0
	for {
		select {
		case ev := <-stream:
			last = ev
			drained++
		default:
			goto done
		}
	}
done:
	if drained == 0 {
		t.Fatal("expected some delivered events")
	}
	if last.Sequence != uint64(subscriberBufferSize+8) {
		t.Fatalf("F72: newest event must survive backpressure, last seq=%d want=%d (drained=%d)",
			last.Sequence, subscriberBufferSize+8, drained)
	}
}

func TestS7B_StoreHistoryIsBounded(t *testing.T) {
	store := NewStore()
	for i := 0; i < maxHistorySize+50; i++ {
		store.Append(Envelope{EventID: "evt", Sequence: uint64(i + 1), Type: "run.started", EmittedAt: time.Now().UTC()})
	}
	history := store.History()
	if len(history) != maxHistorySize {
		t.Fatalf("history len=%d want %d", len(history), maxHistorySize)
	}
	if history[0].Sequence != 51 {
		t.Fatalf("oldest kept seq=%d want 51", history[0].Sequence)
	}
	if history[len(history)-1].Sequence != uint64(maxHistorySize+50) {
		t.Fatalf("newest seq=%d", history[len(history)-1].Sequence)
	}
}
