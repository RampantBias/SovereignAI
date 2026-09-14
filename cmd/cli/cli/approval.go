package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/spf13/cobra"
)

func NewApprovalCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	var requestUID string
	cmd := &cobra.Command{
		Use:     "approval <show|approve|deny> <workflow-id>",
		Short:   "Inspect or submit a human gate decision",
		Example: "sovctl approval approve wf-123 --request-uid id100",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			action := strings.ToLower(strings.TrimSpace(args[0]))
			workflowID := strings.TrimSpace(args[1])
			uid := strings.TrimSpace(requestUID)
			if workflowID == "" {
				return fmt.Errorf("workflow-id is required")
			}
			switch action {
			case "approve", "deny":
				if uid == "" {
					return fmt.Errorf("the '--request-uid' flag is required when action is approve or deny")
				}
			case "show":
			default:
				return fmt.Errorf("invalid action %q; use show, approve, or deny", action)
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			response, err := client.SubmitApproval(ctx, &pb.ApprovalSubmission{
				Action:     action,
				WorkflowId: workflowID,
				RequestUid: uid,
			})
			if err != nil {
				return fmt.Errorf("approval %s failed: %w", action, err)
			}
			if !response.GetSuccess() {
				return fmt.Errorf("approval %s failed: %s", action, response.GetMessage())
			}
			fmt.Fprintln(cmd.OutOrStdout(), response.GetMessage())
			return nil
		},
	}
	cmd.Flags().StringVarP(&requestUID, "request-uid", "r", "", "UID of inspected request (required for approve/deny)")
	return cmd
}
