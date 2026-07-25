package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/spf13/cobra"
)

func NewCreateCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create platform resources (project, workflow, etc.)",
	}

	// Attach the specific subcommands to this parent
	cmd.AddCommand(newCreateProjectCmd(client))
	cmd.AddCommand(newCreateWorkflowCmd(client))

	return cmd
}

/*******************************************************
Sub-Commands
*******************************************************/

func newCreateProjectCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	var infraRepo, appRepo string

	cmd := &cobra.Command{
		Use:   "project [name]",
		Short: "Initialize a new software automation project boundary",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := client.CreateProject(cmd.Context(), &pb.CreateProjectRequest{
				ProjectName: args[0],
				InfraRepo:   infraRepo,
				AppRepo:     appRepo,
			})
			return err
		},
	}

	cmd.Flags().StringVar(&infraRepo, "infra-repo", "", "Git URL for infrastructure repository")
	cmd.Flags().StringVar(&appRepo, "app-repo", "", "Git URL for application repository")
	cmd.MarkFlagRequired("infra-repo")
	cmd.MarkFlagRequired("app-repo")

	return cmd
}

var (
	projectName   string
	manifestPath  string
	changeRequest string
)

func newCreateWorkflowCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workflow",
		Short:   "Initialize a new Sovereign Workflow for a Project",
		Example: `sovctl create workflow --project-name alpha -f workflow-definition.yaml -c ./demo/change-requests/calculator-divide.v1.json`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Try to read workflow manifest file
			manifestFileData, err := os.ReadFile(manifestPath)
			if err != nil {
				return fmt.Errorf("failed to process local workflow definition file: %w", err)
			}

			// Try to read change request file
			changeRequestFileData, err := os.ReadFile(changeRequest)
			if err != nil {
				return fmt.Errorf("failed to process local change-request file: %w", err)
			}

			// Verify the payload is not empty
			if len(manifestFileData) == 0 {
				return fmt.Errorf("provided manifest file %q is empty", manifestPath)
			}
			if len(changeRequestFileData) == 0 {
				return fmt.Errorf("provided change request file %q is empty", manifestPath)
			}

			// Add control plane transit timeout
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			response, err := client.CreateWorkflow(ctx, &pb.CreateWorkflowRequest{
				ProjectName:          projectName,
				ManifestContent:      string(manifestFileData),
				ChangeRequestContent: changeRequestFileData,
			})
			if err != nil {
				return fmt.Errorf("failed to submit workflow: %w", err)
			}

			fmt.Printf("Workflow %s successfully submitted\n", response.WorkflowId)
			return nil
		},
	}

	// Args
	cmd.Flags().StringVarP(&projectName, "project", "p", "", "Target allocation workspace name (Required)")
	cmd.Flags().StringVarP(&manifestPath, "file", "f", "", "Local filepath containing the declarative workflow specification (Required)")
	cmd.Flags().StringVarP(&changeRequest, "change-request", "c", "", "Change request containing the workflow task information (Required)")
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("change-request")

	return cmd
}
