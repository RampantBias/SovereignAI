package audit

import (
	"context"
	"time"
)

// Shared function for adding controller events to auditor
func AppendControllerEvent(ctx context.Context, recorder Recorder, controllerID string, now func() time.Time, options EventOptions) error {
	if recorder == nil {
		return nil
	}
	_, err := BuildAndAppendControllerEvent(ctx, recorder, controllerID, now, options)
	return err
}

// BuildAndAppendControllerEvent applies the standard controller actor and clock
// fields and returns the event so later decisions can reference its stable ID.
func BuildAndAppendControllerEvent(ctx context.Context, recorder Recorder, controllerID string, now func() time.Time, options EventOptions) (Event, error) {
	occurredAt := time.Now().UTC()
	if now != nil {
		occurredAt = now().UTC()
	}
	options.Source = controllerID
	options.OccurredAt = occurredAt
	options.Actor = Actor{Kind: "Controller", ID: controllerID}
	return BuildAndAppendEvent(ctx, recorder, options)
}
