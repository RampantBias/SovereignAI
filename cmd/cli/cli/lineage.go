package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	"github.com/spf13/cobra"
	"os"
)

func NewLineageCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	var event string
	cmd := &cobra.Command{Use: "lineage [workflow-id]", Short: "Project recorded recovery and approval evidence", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		response, err := client.GetWorkflowLineage(cmd.Context(), &pb.GetWorkflowLineageRequest{WorkflowId: args[0], EventId: event})
		if err != nil {
			return err
		}
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, []byte(response.ProjectionJson), "", "  "); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), formatted.String())
		return err
	}}
	cmd.Flags().StringVar(&event, "event", "", "Select one retry or admitted approval event")
	return cmd
}
func NewContextSnapshotCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	var output string
	cmd := &cobra.Command{Use: "context-snapshot [snapshot-id]", Short: "Retrieve private initial context into a local file", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if output == "" {
			return fmt.Errorf("--output is required for private context")
		}
		response, err := client.GetContextSnapshot(cmd.Context(), &pb.GetContextSnapshotRequest{Id: args[0]})
		if err != nil {
			return err
		}
		receipt := response.GetReceipt()
		if receipt == nil || receipt.Id != args[0] || receipt.Digest != artifactcontract.DigestBytes(response.Snapshot) || receipt.EventId != audit.ContextEventID(receipt.Id) {
			return fmt.Errorf("context receipt digest or identity mismatch")
		}
		file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = file.Write(response.Snapshot)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved context %s to %s (digest verified)\n", receipt.Id, output)
		return err
	}}
	cmd.Flags().StringVar(&output, "output", "", "New private file to create")
	return cmd
}
