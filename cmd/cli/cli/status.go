package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/spf13/cobra"
)

func NewStatusCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Display all active workflows",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			resp, err := client.ListWorkflows(ctx, &pb.ListWorkflowsRequest{})
			if err != nil {
				return fmt.Errorf("failed to fetch system status from orchestrator: %w", err)
			}

			w := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "PROJECT\tID\tSTATUS\tSTEP\tNAMESPACE")

			for _, wf := range resp.GetWorkflows() {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					wf.GetProjectName(),
					wf.GetWorkflowId(),
					wf.GetStatus(),
					wf.GetCurrentStep(),
					wf.GetNamespace(),
				)
			}

			w.Flush()
			return nil
		},
	}
}
