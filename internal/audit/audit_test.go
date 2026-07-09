package audit

import (
	"context"
	"testing"
	"time"
)

func TestMemoryRecorderIsIdempotentAndOrdered(t *testing.T) {
	recorder := NewMemoryRecorder()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	late := Event{ID: "b", Type: "StepSucceeded", SchemaVersion: "v1", OccurredAt: base.Add(time.Minute), Subject: Subject{Workflow: "wf-1"}}
	early := Event{ID: "a", Type: "StepStarted", SchemaVersion: "v1", OccurredAt: base, Subject: Subject{Workflow: "wf-1"}}
	for _, event := range []Event{late, early, early} {
		if err := recorder.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := recorder.ListWorkflow(context.Background(), "wf-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ID != "a" || events[1].ID != "b" {
		t.Fatalf("unexpected timeline: %#v", events)
	}
}

func TestDeterministicID(t *testing.T) {
	first := DeterministicID("wf-1", "developer", "1", "started")
	if first != DeterministicID("wf-1", "developer", "1", "started") {
		t.Fatal("same domain keys produced different IDs")
	}
	if first == DeterministicID("wf-1", "developer", "2", "started") {
		t.Fatal("different attempt produced the same ID")
	}
}
