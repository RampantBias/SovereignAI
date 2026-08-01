package audit

import (
	"context"
	"encoding/json"
	"path/filepath"
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

func TestNewEventPreservesRequester(t *testing.T) {
	requester := &Actor{Kind: "User", ID: "frank"}
	event, err := NewEvent(EventOptions{
		Source: "api", Type: "WorkflowSubmitted", Actor: Actor{Kind: "API", ID: "api-server"},
		Requester: requester, Subject: Subject{Project: "platform"}, Action: "submit", Outcome: "accepted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Requester == nil || *event.Requester != *requester {
		t.Fatalf("requester was not preserved: %#v", event.Requester)
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

func TestFileRecorderRoundTripsWorkflowEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	recorder := NewFileRecorder(path)
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{ID: "late", Type: "AgentResultAccepted", SchemaVersion: "v1", OccurredAt: base.Add(time.Minute), Subject: Subject{Workflow: "wf"}},
		{ID: "early", Type: "AgentWrapperStarted", SchemaVersion: "v1", OccurredAt: base, Subject: Subject{Workflow: "wf"}},
		{ID: "other", Type: "AgentWrapperStarted", SchemaVersion: "v1", OccurredAt: base, Subject: Subject{Workflow: "other"}},
	}
	for _, event := range events {
		if err := recorder.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	timeline, err := recorder.ListWorkflow(context.Background(), "wf")
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline) != 2 || timeline[0].ID != "early" || timeline[1].ID != "late" {
		t.Fatalf("unexpected file timeline: %#v", timeline)
	}
	if !recorder.Has("AgentResultAccepted") {
		t.Fatal("expected file recorder to find event type")
	}
}

func TestIngestEventFileAppendsToRecorder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	fileRecorder := NewFileRecorder(path)
	event := Event{
		ID:            "runtime-1",
		Type:          "AgentResultRejected",
		SchemaVersion: "v1",
		OccurredAt:    time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
		Subject:       Subject{Workflow: "wf"},
		Action:        "validate",
		Outcome:       "rejected",
		CorrelationID: "wf",
	}
	if err := fileRecorder.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	recorder := NewMemoryRecorder()
	count, err := IngestEventFile(context.Background(), recorder, path)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || !recorder.Has("AgentResultRejected") {
		t.Fatalf("runtime event was not ingested: count=%d events=%#v", count, recorder.AllEvents())
	}
}
