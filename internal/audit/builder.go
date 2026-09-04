package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Used for building events
type EventOptions struct {
	Source        string
	Type          string
	OccurredAt    time.Time
	Actor         Actor
	Requester     *Actor
	Subject       Subject
	Action        string
	Target        string
	Outcome       string
	Reason        string
	CorrelationID string
	CausationID   string
	DecisionID    string
	References    map[string]string
	Data          any
}

// Lite-factory for building events
// TODO: Align Workflow UID and Name to correlation set and List methodologies
func NewEvent(options EventOptions) (Event, error) {
	payload, err := marshalData(options.Data)
	if err != nil {
		return Event{}, err
	}
	occurredAt := options.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	source := options.Source
	if source == "" {
		source = "unknown"
	}
	correlationID := options.CorrelationID
	if correlationID == "" {
		correlationID = options.Subject.Workflow
	}
	if correlationID == "" {
		correlationID = options.Subject.Project
	}
	return Event{
		ID: DeterministicID(
			source,
			options.Type,
			options.Subject.Project,
			options.Subject.Namespace,
			options.Subject.Workflow,
			options.Subject.Step,
			fmt.Sprint(options.Subject.Attempt),
			options.Action,
			options.Target,
			options.Outcome,
			options.Reason,
		),
		Type:          options.Type,
		SchemaVersion: "v1",
		OccurredAt:    occurredAt.UTC(),
		Actor:         options.Actor,
		Requester:     options.Requester,
		Subject:       options.Subject,
		Action:        options.Action,
		Target:        options.Target,
		Outcome:       options.Outcome,
		Reason:        options.Reason,
		CorrelationID: correlationID,
		CausationID:   options.CausationID,
		DecisionID:    options.DecisionID,
		References:    options.References,
		Data:          payload,
	}, nil
}

func AppendEvent(ctx context.Context, recorder Recorder, options EventOptions) error {
	if recorder == nil {
		return nil
	}
	_, err := BuildAndAppendEvent(ctx, recorder, options)
	return err
}

// BuildAndAppendEvent constructs and appends an event while returning the exact
// event identity. Callers use the returned ID to create explicit lineage edges
// between independently recorded audit events.
func BuildAndAppendEvent(ctx context.Context, recorder Recorder, options EventOptions) (Event, error) {
	event, err := NewEvent(options)
	if err != nil {
		return Event{}, err
	}
	if recorder == nil {
		return event, nil
	}
	if err := recorder.Append(ctx, event); err != nil {
		return Event{}, err
	}
	return event, nil
}

func marshalData(data any) (json.RawMessage, error) {
	if data == nil {
		return nil, nil
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal audit event data: %w", err)
	}
	return payload, nil
}
