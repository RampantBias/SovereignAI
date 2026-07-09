package audit

import (
	"context"
	"encoding/json"
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

func TestBuildTimelineProjectsEventForPlayback(t *testing.T) {
	event := Event{
		ID:            "evt-1",
		Type:          "StepAttemptInterrupted",
		SchemaVersion: "v1",
		OccurredAt:    time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
		Actor:         Actor{Kind: "Controller", ID: "stepattempt-controller"},
		Subject:       Subject{Project: "platform", Namespace: "platform-wf", Workflow: "wf", Step: "developer", Attempt: 1},
		Action:        "interrupt",
		Target:        "developer-001",
		Outcome:       "interrupted",
		Reason:        "AgentPodLost",
		CorrelationID: "wf",
		References:    map[string]string{"pod": "developer-001"},
		Data:          json.RawMessage(`{"retryable":true}`),
	}

	timeline := BuildTimeline([]Event{event})
	if len(timeline) != 1 {
		t.Fatalf("timeline length = %d, want 1", len(timeline))
	}
	if timeline[0].ID != event.ID || timeline[0].Type != event.Type {
		t.Fatalf("identity fields not projected: %#v", timeline[0])
	}
	if timeline[0].Subject.Step != "developer" || timeline[0].Subject.Attempt != 1 {
		t.Fatalf("subject not projected: %#v", timeline[0].Subject)
	}
	if string(timeline[0].Data) != `{"retryable":true}` {
		t.Fatalf("data not projected: %s", timeline[0].Data)
	}
}
