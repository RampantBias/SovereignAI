package cli

import (
	"github.com/spf13/cobra"
)

// Initializing Orchestration software (Bootstrapping)
func NewInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap the cluster resources",
		Args:  cobra.ArbitraryArgs, //
		Run: func(cmd *cobra.Command, args []string) {
			//kubeConfig := args[0]

		},
	}

	cmd.Flags().StringP("namespace", "n", "default", "Target namespace")
	return cmd
}
