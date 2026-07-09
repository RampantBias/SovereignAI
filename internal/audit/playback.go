package audit

import (
	"encoding/json"
	"io"
	"time"
)

// Timeline Event is for event translation (cleaner ownership over Event)
type TimelineEvent struct {
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	SchemaVersion string            `json:"schemaVersion"`
	OccurredAt    time.Time         `json:"occurredAt"`
	RecordedAt    time.Time         `json:"recordedAt,omitempty"`
	Actor         Actor             `json:"actor"`
	Requester     *Actor            `json:"requester,omitempty"`
	Subject       Subject           `json:"subject"`
	Action        string            `json:"action"`
	Target        string            `json:"target,omitempty"`
	Outcome       string            `json:"outcome"`
	Reason        string            `json:"reason,omitempty"`
	CorrelationID string            `json:"correlationId"`
	CausationID   string            `json:"causationId,omitempty"`
	DecisionID    string            `json:"decisionId,omitempty"`
	References    map[string]string `json:"references,omitempty"`
	Data          json.RawMessage   `json:"data,omitempty"`
}

// Outputs all events as TimelineEvents
func BuildTimeline(events []Event) []TimelineEvent {
	timeline := make([]TimelineEvent, 0, len(events))
	for _, event := range events {
		timeline = append(timeline, TimelineEvent{
			ID:            event.ID,
			Type:          event.Type,
			SchemaVersion: event.SchemaVersion,
			OccurredAt:    event.OccurredAt,
			RecordedAt:    event.RecordedAt,
			Actor:         event.Actor,
			Requester:     event.Requester,
			Subject:       event.Subject,
			Action:        event.Action,
			Target:        event.Target,
			Outcome:       event.Outcome,
			Reason:        event.Reason,
			CorrelationID: event.CorrelationID,
			CausationID:   event.CausationID,
			DecisionID:    event.DecisionID,
			References:    event.References,
			Data:          event.Data,
		})
	}
	return timeline
}

// WriteTimeline outputs all timeline event properties by default.
// Presentation flags can later narrow this to selected columns without changing storage.
func WriteTimeline(out io.Writer, events []TimelineEvent) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(events)
}
