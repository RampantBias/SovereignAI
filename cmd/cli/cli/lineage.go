package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/lineagedisplay"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

func NewLineageCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	var event, view, format, output string
	cmd := &cobra.Command{Use: "lineage [workflow-id]", Short: "Inspect lineage as JSON or an interactive workflow HTML export", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		selectedView := view
		if format == "html" && !cmd.Flags().Changed("view") {
			selectedView = "workflow"
		}
		if selectedView != "stories" && selectedView != "workflow" {
			return fmt.Errorf("--view must be stories or workflow")
		}
		if format != "json" && format != "html" {
			return fmt.Errorf("--format must be json or html")
		}
		if selectedView == "workflow" && event != "" {
			return fmt.Errorf("--event requires --view stories")
		}
		if format == "html" && (selectedView != "workflow" || output == "") {
			return fmt.Errorf("HTML requires the workflow view and --output")
		}
		response, err := client.GetWorkflowLineage(cmd.Context(), &pb.GetWorkflowLineageRequest{WorkflowId: args[0], EventId: event, View: selectedView})
		if err != nil {
			return err
		}
		var formatted bytes.Buffer
		if format == "html" {
			var projection audit.WorkflowView
			if err := json.Unmarshal([]byte(response.ProjectionJson), &projection); err != nil {
				return err
			}
			if err := lineagedisplay.Render(&formatted, projection); err != nil {
				return err
			}
		} else {
			if err := json.Indent(&formatted, []byte(response.ProjectionJson), "", "  "); err != nil {
				return err
			}
		}
		if output != "" && output != "-" {
			if err := writeLineageExport(output, formatted.Bytes()); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved lineage to %s. Regenerate and reload to refresh.\n", output)
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), formatted.String())
		return err
	}}
	cmd.Flags().StringVar(&event, "event", "", "Select one retry or admitted approval event")
	cmd.Flags().StringVar(&view, "view", "stories", "stories (audit only) or workflow (live navigation plus audit evidence)")
	cmd.Flags().StringVar(&format, "format", "json", "json or html (defaults to workflow view)")
	cmd.Flags().StringVar(&output, "output", "", "Export file in the CLI filesystem, or - for stdout; replaces an earlier file after rendering")
	return cmd
}

// Render first and replace from the same directory, preserving the previous
// presentation export if fetching, encoding, or writing the new snapshot fails.
func writeLineageExport(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".lineage-*")
	if err != nil {
		return fmt.Errorf("create lineage export %q: directory %q must exist and be writable in the CLI filesystem; use --output - for shell redirection: %w", path, dir, err)
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
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
