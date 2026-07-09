package cli

import (
	"fmt"

	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/spf13/cobra"
)

var projectNameFlag string

func NewCleanCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	var cmd = &cobra.Command{
		Use:   "clean [workflowID]",
		Short: "Clean all resources associated with a workflow",
		Args:  cobra.ExactArgs(1), // Ensures the user provides a name
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]
			ctx := cmd.Context()
			fmt.Printf("Requesting purge for workflow %s...\n", workflowID)

			response, err := client.CleanWorkflow(ctx, &pb.CleanWorkflowRequest{
				WorkflowId:  workflowID,
				ProjectName: projectNameFlag,
			})
			if err != nil {
				return fmt.Errorf("remote clean call failed: %w", err)
			}

			if !response.GetSuccess() {
				return fmt.Errorf("service failed to clean workflow: %s", response.GetMessage())
			}
			return nil
		},
	}

	// Require project name flag
	cmd.Flags().StringVarP(&projectNameFlag, "project", "p", "", "The target project parent boundary (Required)")
	cmd.MarkFlagRequired("project")

	return cmd
}
