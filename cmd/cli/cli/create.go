package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
	projectName  string
	manifestPath string
	serverAddr   string
)

func newCreateWorkflowCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workflow",
		Short:   "Initialize a new Sovereign Workflow for a Project",
		Example: `sovctl create workflow --project-name alpha -f workflow-definition.yaml`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Try to read file
			fileData, err := os.ReadFile(manifestPath)
			if err != nil {
				return fmt.Errorf("failed to process local workflow definition file: %w", err)
			}

			// Verify the payload is not empty
			if len(fileData) == 0 {
				return fmt.Errorf("provided manifest file %q is empty", manifestPath)
			}

			// Add control plane transit timeout
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// TODO: Swap with OIDC / TLS per-rpc [later]
			conn, err := grpc.NewClient(serverAddr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				return fmt.Errorf("failed to lock link to orchestrator API server: %w", err)
			}
			defer conn.Close()

			response, err := client.CreateWorkflow(ctx, &pb.CreateWorkflowRequest{
				ProjectName:     projectName,
				ManifestContent: string(fileData),
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
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("file")

	cmd.Flags().StringVar(&serverAddr, "server", "127.0.0.1:8090", "Target address endpoint of the local Orchestrator")

	return cmd
}
