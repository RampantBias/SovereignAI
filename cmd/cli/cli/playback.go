package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/audit"
	"github.com/spf13/cobra"
)

func NewPlaybackCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	return &cobra.Command{
		Use:   "playback [workflow-id]",
		Short: "Display the audit timeline for a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			response, err := client.GetWorkflowTimeline(cmd.Context(), &pb.GetWorkflowTimelineRequest{
				WorkflowId: args[0],
			})
			if err != nil {
				return fmt.Errorf("failed to fetch workflow timeline: %w", err)
			}

			out := cmd.OutOrStdout()
			if len(response.GetEvents()) == 0 {
				fmt.Fprintf(out, "No audit events recorded for workflow %s\n", args[0])
				return nil
			}

			events := make([]audit.TimelineEvent, 0, len(response.GetEvents()))
			for _, dto := range response.GetEvents() {
				event, err := timelineEventFromDTO(dto)
				if err != nil {
					return fmt.Errorf("decode audit event %q: %w", dto.GetId(), err)
				}
				events = append(events, event)
			}
			return audit.WriteTimeline(out, events)
		},
	}
}

// Helper converts protobuf AuditEventDTO to TimelineEvent for playback
func timelineEventFromDTO(dto *pb.AuditEventDTO) (audit.TimelineEvent, error) {
	occurredAt, err := parseAuditTime(dto.GetOccurredAt())
	if err != nil {
		return audit.TimelineEvent{}, err
	}
	recordedAt, err := parseAuditTime(dto.GetRecordedAt())
	if err != nil {
		return audit.TimelineEvent{}, err
	}

	var data json.RawMessage
	if dto.GetDataJson() != "" {
		data = json.RawMessage(dto.GetDataJson())
	}

	event := audit.TimelineEvent{
		ID:            dto.GetId(),
		Type:          dto.GetType(),
		SchemaVersion: dto.GetSchemaVersion(),
		OccurredAt:    occurredAt,
		RecordedAt:    recordedAt,
		Action:        dto.GetAction(),
		Target:        dto.GetTarget(),
		Outcome:       dto.GetOutcome(),
		Reason:        dto.GetReason(),
		CorrelationID: dto.GetCorrelationId(),
		CausationID:   dto.GetCausationId(),
		DecisionID:    dto.GetDecisionId(),
		References:    dto.GetReferences(),
		Data:          data,
	}
	if actor := dto.GetActor(); actor != nil {
		event.Actor = audit.Actor{Kind: actor.GetKind(), ID: actor.GetId()}
	}
	if subject := dto.GetSubject(); subject != nil {
		event.Subject = audit.Subject{
			Project:   subject.GetProject(),
			Namespace: subject.GetNamespace(),
			Workflow:  subject.GetWorkflow(),
			Step:      subject.GetStep(),
			Attempt:   subject.GetAttempt(),
		}
	}
	return event, nil
}

func parseAuditTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}
