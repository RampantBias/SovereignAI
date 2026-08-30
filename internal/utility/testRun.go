package utility

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SovereignAI/internal/artifactcontract"
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
	if outputContract(input) == artifactcontract.TestReportContract {
		if _, err := requiredInput(input, artifactcontract.PreparedCandidateContract); err != nil {
			return err
		}
		if _, err := parameter(input, "environmentImageDigest"); err != nil {
			return err
		}
	}
	return nil
}

func (TestRun) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	if outputContract(input) != artifactcontract.TestReportContract {
		output, err := runTemplateCommand(ctx, input)
		if err != nil {
			return utilitycontract.Result{}, err
		}
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

	preparedRef, prepared, err := readInputArtifact[artifactcontract.PreparedCandidate](input, artifactcontract.PreparedCandidateContract)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if err := prepared.Validate(); err != nil {
		return utilitycontract.Result{}, fmt.Errorf("invalid prepared-candidate input: %w", err)
	}
	if err := verifyPreparedWorkspace(ctx, input.WorkspacePath, prepared); err != nil {
		return utilitycontract.Result{}, err
	}

	started := time.Now()
	output, exitCode, commandErr := runObservedTemplateCommand(ctx, input)
	duration := time.Since(started).Milliseconds()
	if len(output.Stdout)+len(output.Stderr) > artifactcontract.MaxCapturedTestOutputBytes {
		return utilitycontract.Result{}, fmt.Errorf("captured test output exceeds %d bytes", artifactcontract.MaxCapturedTestOutputBytes)
	}
	observedTree, treeErr := gitOutput(ctx, input.WorkspacePath, "write-tree")
	if treeErr != nil {
		return utilitycontract.Result{}, treeErr
	}
	workspaceClean := observedTree == prepared.CandidateTree && workspaceHasNoTestDrift(ctx, input.WorkspacePath)
	outcome := "passed"
	if commandErr != nil {
		outcome = "environment-error"
		if exitCode != nil {
			outcome = "failed"
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		outcome = "timed-out"
		exitCode = nil
	}
	if !workspaceClean {
		outcome = "tree-drift"
	}
	report := artifactcontract.TestReport{
		PreparedCandidateDigest: preparedRef.Digest,
		BaseCommit:              prepared.BaseCommit,
		CandidateTree:           prepared.CandidateTree,
		ObservedTreeAfter:       observedTree,
		WorkspaceClean:          workspaceClean,
		CommandDigest:           artifactcontract.DigestBytes([]byte(strings.Join(input.Command, "\x00"))),
		EnvironmentImageDigest:  input.Parameters["environmentImageDigest"],
		Outcome:                 outcome,
		ExitCode:                exitCode,
		DurationMilliseconds:    duration,
		Stdout:                  streamEvidence([]byte(output.Stdout)),
		Stderr:                  streamEvidence([]byte(output.Stderr)),
	}
	if err := report.Validate(); err != nil {
		return utilitycontract.Result{}, fmt.Errorf("validate test report: %w", err)
	}
	artifacts, err := writeOperationArtifact(input, report)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	result := utilitycontract.Result{
		SchemaVersion: utilitycontract.Version,
		Outcome:       "Succeeded",
		Message:       "project test command passed against the prepared candidate",
		Artifacts:     artifacts,
		Metadata:      map[string]string{"tree": observedTree, "testOutcome": outcome},
	}
	if outcome != "passed" {
		message := "project test command produced " + outcome
		if commandErr != nil {
			message += ": " + commandErr.Error()
		}
		result.Outcome = "Failed"
		result.Message = message
		failureCode := map[string]string{"failed": "Failed", "timed-out": "TimedOut", "environment-error": "EnvironmentError", "tree-drift": "TreeDrift"}[outcome]
		result.Error = &utilitycontract.ResultError{Code: "TestRun" + failureCode, Message: message}
		if outcome == "failed" {
			if diagnostic := goTestDiagnostic(input, output); diagnostic != "" {
				result.Message = diagnostic
				result.Error = &utilitycontract.ResultError{Code: utilitycontract.TestRunCodeError, Message: diagnostic}
			}
		}
	}
	return result, nil
}

func runObservedTemplateCommand(ctx context.Context, input utilitycontract.Input) (commandOutput, *int, error) {
	cmd := exec.CommandContext(ctx, input.Command[0], input.Command[1:]...)
	cmd.Dir = input.WorkspacePath
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	output := commandOutput{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		zero := 0
		return output, &zero, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		code := exitError.ExitCode()
		return output, &code, err
	}
	return output, nil, err
}

func workspaceHasNoTestDrift(ctx context.Context, workspace string) bool {
	if _, err := runGit(ctx, workspace, "diff", "--quiet"); err != nil {
		return false
	}
	return workspaceHasNoUnexpectedUntracked(ctx, workspace)
}

func workspaceHasNoUnexpectedUntracked(ctx context.Context, workspace string) bool {
	untracked, err := gitOutput(ctx, workspace, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return false
	}
	for _, path := range strings.Split(untracked, "\n") {
		path = strings.TrimSpace(path)
		if path != "" && !strings.HasPrefix(path, "attempts/") && !strings.HasPrefix(path, ".sovereign/") {
			return false
		}
	}
	return true
}

func streamEvidence(data []byte) artifactcontract.StreamEvidence {
	excerpt := []byte(strings.ToValidUTF8(string(data), "?"))
	if len(excerpt) > artifactcontract.MaxTestOutputExcerptBytes {
		excerpt = excerpt[:artifactcontract.MaxTestOutputExcerptBytes]
		for len(excerpt) > 0 && !utf8.Valid(excerpt) {
			excerpt = excerpt[:len(excerpt)-1]
		}
	}
	return artifactcontract.StreamEvidence{
		Digest: artifactcontract.DigestBytes(data), CapturedBytes: len(data), Excerpt: string(excerpt), Truncated: len(excerpt) < len(data),
	}
}
