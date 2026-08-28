package utility

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

type CandidatePrepare struct{}

func (CandidatePrepare) Name() string { return OperationCandidatePrepare }

func (CandidatePrepare) Validate(input utilitycontract.Input) error {
	if err := ensureWorkspace(input); err != nil {
		return err
	}
	if outputContract(input) != artifactcontract.PreparedCandidateContract {
		return fmt.Errorf("candidate.prepare requires a prepared-candidate/v1 output")
	}
	for _, contract := range []string{
		artifactcontract.RepositoryRevisionContract,
		artifactcontract.TestChangeSetContract,
		artifactcontract.ChangeSetContract,
	} {
		if _, err := requiredInput(input, contract); err != nil {
			return err
		}
	}
	return nil
}

func (CandidatePrepare) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	repositoryRef, repository, err := readInputArtifact[artifactcontract.RepositoryRevision](input, artifactcontract.RepositoryRevisionContract)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	testRef, tests, err := readInputArtifact[artifactcontract.TestChangeSet](input, artifactcontract.TestChangeSetContract)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	changeRef, change, err := readInputArtifact[artifactcontract.ChangeSet](input, artifactcontract.ChangeSetContract)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if err := repository.Validate(); err != nil {
		return utilitycontract.Result{}, fmt.Errorf("invalid repository-revision input: %w", err)
	}
	if err := tests.Validate(); err != nil {
		return utilitycontract.Result{}, fmt.Errorf("invalid test-change-set input: %w", err)
	}
	if err := change.Validate(); err != nil {
		return utilitycontract.Result{}, fmt.Errorf("invalid change-set input: %w", err)
	}
	if tests.BaseCommit != repository.ResolvedCommit || change.BaseCommit != repository.ResolvedCommit ||
		tests.ChangeRequestDigest != change.ChangeRequestDigest ||
		tests.ImplementationPlanDigest != change.ImplementationPlanDigest {
		return utilitycontract.Result{}, fmt.Errorf("candidate inputs do not share one source and plan lineage")
	}

	actualRemote, err := gitOutput(ctx, input.WorkspacePath, "config", "--get", "remote.origin.url")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if actualRemote != repository.RepositoryURL {
		return utilitycontract.Result{}, fmt.Errorf("workspace origin %q does not match repository-revision %q", actualRemote, repository.RepositoryURL)
	}
	resolvedBase, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", repository.ResolvedCommit+"^{commit}")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if resolvedBase != repository.ResolvedCommit {
		return utilitycontract.Result{}, fmt.Errorf("repository-revision base commit is not available exactly")
	}

	branch := "sovereign/" + sha256String(input.WorkflowID)[:20]
	commandDigest := artifactcontract.DigestBytes([]byte(strings.Join([]string{
		"candidate.prepare/v1", repositoryRef.Digest, testRef.Digest, changeRef.Digest, branch,
	}, "\x00")))
	markerPath := filepath.Join(input.WorkspacePath, ".git", "sovereign", "candidates", sha256String(input.IdempotencyKey)+".json")
	if candidate, found, err := readPreparedCandidateMarker(markerPath); err != nil {
		return utilitycontract.Result{}, err
	} else if found {
		if candidate.RepositoryRevisionDigest != repositoryRef.Digest || candidate.TestChangeSetDigest != testRef.Digest ||
			candidate.ChangeSetDigest != changeRef.Digest || candidate.Branch != branch || candidate.PreparationCommandDigest != commandDigest {
			return utilitycontract.Result{}, fmt.Errorf("idempotency key %q already belongs to different candidate inputs", input.IdempotencyKey)
		}
		if err := verifyPreparedWorkspace(ctx, input.WorkspacePath, candidate); err != nil {
			return utilitycontract.Result{}, err
		}
		return preparedCandidateResult(input, candidate, "candidate already prepared")
	}

	if err := requireCleanBaseWorkspace(ctx, input.WorkspacePath, repository.ResolvedCommit); err != nil {
		return utilitycontract.Result{}, err
	}
	if _, err := runGit(ctx, input.WorkspacePath, "checkout", "-B", branch, repository.ResolvedCommit); err != nil {
		return utilitycontract.Result{}, err
	}
	for _, patch := range []struct {
		name string
		body string
	}{{"test-change-set", tests.Patch}, {"change-set", change.Patch}} {
		if _, err := runGitWithInput(ctx, input.WorkspacePath, []byte(patch.body), "apply", "--check", "--index"); err != nil {
			return utilitycontract.Result{}, fmt.Errorf("validate %s patch: %w", patch.name, err)
		}
		if _, err := runGitWithInput(ctx, input.WorkspacePath, []byte(patch.body), "apply", "--index"); err != nil {
			return utilitycontract.Result{}, fmt.Errorf("apply %s patch: %w", patch.name, err)
		}
	}
	tree, err := gitOutput(ctx, input.WorkspacePath, "write-tree")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	changedPaths := append(append([]string(nil), tests.Files...), change.Files...)
	slices.Sort(changedPaths)
	changedPaths = slices.Compact(changedPaths)
	candidate := artifactcontract.PreparedCandidate{
		RepositoryRevisionDigest: repositoryRef.Digest,
		TestChangeSetDigest:      testRef.Digest,
		ChangeSetDigest:          changeRef.Digest,
		BaseCommit:               repository.ResolvedCommit,
		Branch:                   branch,
		CandidateTree:            tree,
		ChangedPaths:             changedPaths,
		PreparationCommandDigest: commandDigest,
	}
	if err := candidate.Validate(); err != nil {
		return utilitycontract.Result{}, fmt.Errorf("validate prepared candidate: %w", err)
	}
	if err := writePreparedCandidateMarker(markerPath, candidate); err != nil {
		return utilitycontract.Result{}, err
	}
	return preparedCandidateResult(input, candidate, "candidate prepared")
}

func requireCleanBaseWorkspace(ctx context.Context, workspace, base string) error {
	head, err := gitOutput(ctx, workspace, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return err
	}
	status, err := gitOutput(ctx, workspace, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	if head != base || status != "" || !workspaceHasNoUnexpectedUntracked(ctx, workspace) {
		return fmt.Errorf("candidate.prepare requires a clean repository at accepted source commit %s", base)
	}
	return nil
}

func verifyPreparedWorkspace(ctx context.Context, workspace string, candidate artifactcontract.PreparedCandidate) error {
	branch, err := gitOutput(ctx, workspace, "branch", "--show-current")
	if err != nil {
		return err
	}
	tree, err := gitOutput(ctx, workspace, "write-tree")
	if err != nil {
		return err
	}
	if branch != candidate.Branch || tree != candidate.CandidateTree {
		return fmt.Errorf("prepared workspace no longer matches the recorded candidate")
	}
	if _, err := runGit(ctx, workspace, "diff", "--quiet"); err != nil || !workspaceHasNoUnexpectedUntracked(ctx, workspace) {
		return fmt.Errorf("prepared workspace has unstaged tree drift")
	}
	return nil
}

func readPreparedCandidateMarker(path string) (artifactcontract.PreparedCandidate, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return artifactcontract.PreparedCandidate{}, false, nil
	}
	if err != nil {
		return artifactcontract.PreparedCandidate{}, false, fmt.Errorf("read candidate idempotency marker: %w", err)
	}
	var candidate artifactcontract.PreparedCandidate
	if err := json.Unmarshal(data, &candidate); err != nil {
		return candidate, false, fmt.Errorf("decode candidate idempotency marker: %w", err)
	}
	if err := candidate.Validate(); err != nil {
		return candidate, false, fmt.Errorf("validate candidate idempotency marker: %w", err)
	}
	return candidate, true, nil
}

func writePreparedCandidateMarker(path string, candidate artifactcontract.PreparedCandidate) error {
	data, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create candidate marker directory: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o640); err != nil {
		return fmt.Errorf("write candidate marker: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish candidate marker: %w", err)
	}
	return nil
}

func preparedCandidateResult(input utilitycontract.Input, candidate artifactcontract.PreparedCandidate, message string) (utilitycontract.Result, error) {
	artifacts, err := writeOperationArtifact(input, candidate)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	return utilitycontract.Result{
		SchemaVersion: utilitycontract.Version,
		Outcome:       "Succeeded",
		Message:       message,
		Artifacts:     artifacts,
		Metadata: map[string]string{
			"branch": candidate.Branch, "baseCommit": candidate.BaseCommit, "tree": candidate.CandidateTree,
		},
	}, nil
}
