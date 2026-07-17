package controllers

import (
	"context"
	"time"

	"github.com/SovereignAI/internal/audit"
)

// Shared function for adding controller events to auditor
func appendControllerEvent(ctx context.Context, recorder audit.Recorder, controllerID string, now func() time.Time, options audit.EventOptions) error {
	if recorder == nil {
		return nil
	}
	occurredAt := time.Now().UTC()
	if now != nil {
		occurredAt = now().UTC()
	}
	options.Source = controllerID
	options.OccurredAt = occurredAt
	options.Actor = audit.Actor{Kind: "Controller", ID: controllerID}
	return audit.AppendEvent(ctx, recorder, options)
}
