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
	occurredAt := time.Now().UTC()
	if now != nil {
		occurredAt = now().UTC()
	}
	options.Source = controllerID
	options.OccurredAt = occurredAt
	options.Actor = Actor{Kind: "Controller", ID: controllerID}
	return AppendEvent(ctx, recorder, options)
}
