package store_test

import (
	"testing"
	"time"

	"github.com/doomsday/agent/internal/store"
)

func TestRoundTrip(t *testing.T) {
	s, err := store.OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ev := store.Event{
		EventID:      "test-001",
		CapturedAt:   time.Now(),
		EndpointType: "completion",
		PayloadJSON:  `{"event_id":"test-001"}`,
	}

	if err := s.Insert(ev); err != nil {
		t.Fatalf("insert: %v", err)
	}

	count, err := s.UnsyncedCount()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 unsynced, got %d", count)
	}

	events, err := s.Unsynced(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventID != "test-001" {
		t.Fatalf("unexpected events: %+v", events)
	}

	if err := s.MarkSynced([]string{"test-001"}); err != nil {
		t.Fatal(err)
	}

	count, _ = s.UnsyncedCount()
	if count != 0 {
		t.Fatalf("expected 0 after mark synced, got %d", count)
	}
}

func TestIdempotentInsert(t *testing.T) {
	s, err := store.OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ev := store.Event{EventID: "dup-001", CapturedAt: time.Now(), EndpointType: "completion", PayloadJSON: "{}"}
	_ = s.Insert(ev)
	_ = s.Insert(ev) // should be ignored

	count, _ := s.UnsyncedCount()
	if count != 1 {
		t.Fatalf("expected 1 after duplicate insert, got %d", count)
	}
}
