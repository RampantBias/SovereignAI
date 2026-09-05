package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/spf13/cobra"
)

type ShellDependencies struct {
	Client pb.OrchestratorServiceClient
	Cancel context.CancelFunc
}

func RunShell(ctx context.Context, shellDependencies ShellDependencies) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Sovereign Orchestrator Shell Ready.")

	for {
		fmt.Print("sovereign-orchestrator> ")
		input, _ := reader.ReadString('\n')
		line := strings.TrimSpace(input)
		if line == "exit" || line == "quit" {
			shellDependencies.Cancel()
			return
		}
		args := strings.Fields(line)
		if len(args) == 0 {
			continue
		}
		// Each shell submission gets fresh flags, including the inspected approval UID.
		rootCmd := NewRootCmd(shellDependencies.Client)
		rootCmd.SetArgs(args)
		if err := rootCmd.ExecuteContext(ctx); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	}
}

func NewRootCmd(client pb.OrchestratorServiceClient) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "sovctl",
		Short:         "Sovereign Orchestrator CLI",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	rootCmd.AddCommand(
		NewStatusCmd(client),
		NewCreateCmd(client),
		NewCleanCmd(client),
		NewPlaybackCmd(client),
		NewApprovalCmd(client),
	)
	return rootCmd
}
