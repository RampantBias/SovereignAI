package utility

import (
	"context"
	"fmt"

	"github.com/SovereignAI/internal/utilitycontract"
)

type TestRun struct{}

func (TestRun) Name() string { return OperationTestRun }

func (TestRun) Validate(input utilitycontract.Input) error {
	if err := ensureWorkspace(input); err != nil {
		return err
	}
	if len(input.Command) == 0 {
		return fmt.Errorf("test.run requires a Project-owned test command")
	}
	return nil
}

func (TestRun) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	output, err := runTemplateCommand(ctx, input)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	// Commit is provenance metadata for a repository-backed workspace, not a
	// prerequisite for executing the Project-owned test template. This keeps the
	// operation usable for admitted workspaces that are not Git repositories.
	commit, _ := gitOutput(ctx, input.WorkspacePath, "rev-parse", "HEAD")
	artifacts, err := writeOperationArtifact(input, map[string]any{
		"operation": input.Operation, "idempotencyKey": input.IdempotencyKey,
		"command": input.Command, "commit": commit, "stdout": output.Stdout, "stderr": output.Stderr,
	})
	if err != nil {
		return utilitycontract.Result{}, err
	}
	return utilitycontract.Result{
		SchemaVersion: utilitycontract.Version, Outcome: "Succeeded", Message: "project test command succeeded",
		Artifacts: artifacts, Metadata: map[string]string{"commit": commit},
	}, nil
}
