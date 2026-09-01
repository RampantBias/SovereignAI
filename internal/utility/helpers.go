package utility

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/SovereignAI/internal/utilitycontract"
)

type commandOutput struct {
	Stdout string
	Stderr string
}

// writeOperationArtifact publishes the operation's structured evidence into the
// attempt staging area. The collector, rather than the utility process, later
// validates, hashes, and registers this file as an Artifact resource.
func writeOperationArtifact(input utilitycontract.Input, payload any) ([]utilitycontract.ArtifactOutput, error) {
	if len(input.Outputs) == 0 {
		return nil, nil
	}
	output := input.Outputs[0]
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode operation artifact: %w", err)
	}
	name := strings.ReplaceAll(output.Name, "/", "-") + ".json"
	path := filepath.Join(input.StagingPath, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create operation artifact directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return nil, fmt.Errorf("write operation artifact: %w", err)
	}
	return []utilitycontract.ArtifactOutput{{
		Contract:  output.Contract(),
		Path:      path,
		MediaType: mediaType(output.MediaType, "application/json"),
	}}, nil
}

func runCommand(ctx context.Context, workspace string, name string, args ...string) (commandOutput, error) {
	return runCommandWithInput(ctx, workspace, nil, name, args...)
}

func runCommandWithInput(ctx context.Context, workspace string, input []byte, name string, args ...string) (commandOutput, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workspace
	// Utility-created commits use a platform identity. The workflow, attempt,
	// policy decision, and human/agent provenance remain in the surrounding
	// utility contract and generated evidence rather than impersonating a user.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=SovereignAI Utility Runner",
		"GIT_AUTHOR_EMAIL=sovereign-ai@example.invalid",
		"GIT_COMMITTER_NAME=SovereignAI Utility Runner",
		"GIT_COMMITTER_EMAIL=sovereign-ai@example.invalid",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := commandOutput{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		return output, fmt.Errorf("%s %s failed: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(output.Stderr))
	}
	return output, nil
}

func runGit(ctx context.Context, workspace string, args ...string) (commandOutput, error) {
	return runCommand(ctx, workspace, "git", gitCommandArgs(workspace, args...)...)
}

func runGitWithInput(ctx context.Context, workspace string, input []byte, args ...string) (commandOutput, error) {
	return runCommandWithInput(ctx, workspace, input, "git", gitCommandArgs(workspace, args...)...)
}

func gitCommandArgs(workspace string, args ...string) []string {
	commandArgs := make([]string, 0, len(args)+2)
	commandArgs = append(commandArgs, "-c", "safe.directory="+workspace)
	return append(commandArgs, args...)
}

// gitOutput is the query-oriented Git adapter. It preserves runGit's exit-code
// handling but returns only trimmed stdout, which is convenient for commands
// such as rev-parse and remote get-url whose stdout is the value being observed.
func gitOutput(ctx context.Context, workspace string, args ...string) (string, error) {
	output, err := runGit(ctx, workspace, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output.Stdout), nil
}

func EnsureWorkspace(input utilitycontract.Input) error {
	info, err := os.Stat(input.WorkspacePath)
	if err != nil {
		return fmt.Errorf("inspect workspace: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspacePath must be a directory")
	}
	return nil
}

func parameter(input utilitycontract.Input, name string) (string, error) {
	value := strings.TrimSpace(input.Parameters[name])
	if value == "" {
		return "", fmt.Errorf("%s parameter is required", name)
	}
	return value, nil
}

func OptionalParameter(input utilitycontract.Input, name, fallback string) string {
	if value := strings.TrimSpace(input.Parameters[name]); value != "" {
		return value
	}
	return fallback
}

func verifyAdmittedRepository(ctx context.Context, input utilitycontract.Input) error {
	// repositoryURL was resolved by the controller from the admitted Project.
	// Reading origin again at execution time prevents a modified workspace from
	// redirecting a privileged Git or build operation to a different repository.
	// This check binds the remote identity only; revision/tree checks belong to
	// the individual operation.
	expected, err := parameter(input, "repositoryURL")
	if err != nil {
		return err
	}
	actual, err := gitOutput(ctx, input.WorkspacePath, "config", "--get", "remote.origin.url")
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("workspace origin %q does not match admitted repository %q", actual, expected)
	}
	return nil
}

func runTemplateCommand(ctx context.Context, input utilitycontract.Input) (commandOutput, error) {
	// Command is resolved from the Project's test/build template before the Job
	// is created. The runner executes that admitted template; it does not accept
	// a workflow-supplied shell expression here.
	if len(input.Command) == 0 || strings.TrimSpace(input.Command[0]) == "" {
		return commandOutput{}, fmt.Errorf("%s requires a Project-owned command template", input.Operation)
	}
	return runCommand(ctx, input.WorkspacePath, input.Command[0], input.Command[1:]...)
}

func hasGitCommit(ctx context.Context, workspace, key string) (string, bool, error) {
	// The idempotency trailer makes an accepted Git effect discoverable after a
	// runner or controller restart. The bounded history scan is the MVP lookup;
	// the matching tree is checked by the caller before the commit is reused.
	log, err := gitOutput(ctx, workspace, "log", "--all", "--format=%H%x00%B%x00END", "-n", "100")
	if err != nil {
		return "", false, nil
	}
	records := strings.Split(log, "\x00END\n")
	for _, record := range records {
		if strings.Contains(record, key) {
			parts := strings.SplitN(record, "\x00", 2)
			if parts[0] != "" {
				return strings.TrimSpace(parts[0]), true, nil
			}
		}
	}
	return "", false, nil
}

func mediaType(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
