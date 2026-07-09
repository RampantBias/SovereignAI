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
	// 1. Initialize Root
	rootCmd := &cobra.Command{
		Use:   "sovereign-orchestrator",
		Short: "Sovereign Orchestrator CLI",
		// Silence errors/usage so they don't crash the shell loop
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// Add commands with injected dependencies
	rootCmd.AddCommand(
		NewStatusCmd(shellDependencies.Client),
		NewCreateCmd(shellDependencies.Client),
		NewCleanCmd(shellDependencies.Client),
		NewPlaybackCmd(shellDependencies.Client),
	)

	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Sovereign Orchestrator Shell Ready.")

	for {
		fmt.Print("sovereign-orchestrator> ")

		// Read input (simplified for brevity, use the channel method for Ctrl+C safety)
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

		// 3. Execute
		rootCmd.SetArgs(args)
		if err := rootCmd.Execute(); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	}
}
