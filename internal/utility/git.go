package utility

import (
	"context"
	"fmt"
	"strings"

	"github.com/SovereignAI/internal/utilitycontract"
)

type GitCreateBranch struct{}

func (GitCreateBranch) Name() string {
	return OperationGitCreateBranch
}

func (GitCreateBranch) Validate(input utilitycontract.Input) error {
	if err := ensureWorkspace(input); err != nil {
		return err
	}
	_, err := parameter(input, "branch")
	if err != nil {
		return err
	}
	_, err = parameter(input, "repositoryURL")
	return err
}

func (GitCreateBranch) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	if err := verifyAdmittedRepository(ctx, input); err != nil {
		return utilitycontract.Result{}, err
	}
	branch, _ := parameter(input, "branch")
	if _, err := runGit(ctx, input.WorkspacePath, "check-ref-format", "--branch", branch); err != nil {
		return utilitycontract.Result{}, fmt.Errorf("invalid branch %q: %w", branch, err)
	}
	baseRevision := optionalParameter(input, "baseRevision", "HEAD")
	if _, err := runGit(ctx, input.WorkspacePath, "rev-parse", "--verify", branch); err == nil {
		if _, err := runGit(ctx, input.WorkspacePath, "merge-base", "--is-ancestor", baseRevision, branch); err != nil {
			return utilitycontract.Result{}, fmt.Errorf("existing branch %q is not based on %q", branch, baseRevision)
		}
		if _, err := runGit(ctx, input.WorkspacePath, "checkout", branch); err != nil {
			return utilitycontract.Result{}, err
		}
		return gitArtifactResult(input, "branch already existed", map[string]string{"branch": branch})
	}
	args := []string{"checkout", "-b", branch, baseRevision}
	if _, err := runGit(ctx, input.WorkspacePath, args...); err != nil {
		return utilitycontract.Result{}, err
	}
	return gitArtifactResult(input, "branch created", map[string]string{"branch": branch, "baseRevision": baseRevision})
}

type GitCommit struct{}

func (GitCommit) Name() string {
	return OperationGitCommit
}

func (GitCommit) Validate(input utilitycontract.Input) error {
	if err := ensureWorkspace(input); err != nil {
		return err
	}
	_, err := parameter(input, "message")
	if err != nil {
		return err
	}
	_, err = parameter(input, "repositoryURL")
	return err
}

func (GitCommit) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	if err := verifyAdmittedRepository(ctx, input); err != nil {
		return utilitycontract.Result{}, err
	}
	if _, err := runGit(ctx, input.WorkspacePath, "add", "-A"); err != nil {
		return utilitycontract.Result{}, err
	}
	// write-tree records the exact staged content without creating a commit. It
	// lets a retry prove that an existing idempotent commit represents the same
	// requested effect before that commit is re-used.
	requestedTree, err := gitOutput(ctx, input.WorkspacePath, "write-tree")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if commit, ok, err := hasGitCommit(ctx, input.WorkspacePath, input.IdempotencyKey); err != nil {
		return utilitycontract.Result{}, err
	} else if ok {
		committedTree, treeErr := gitOutput(ctx, input.WorkspacePath, "rev-parse", commit+"^{tree}")
		if treeErr != nil {
			return utilitycontract.Result{}, treeErr
		}
		if committedTree != requestedTree {
			return utilitycontract.Result{}, fmt.Errorf("idempotency key %q already belongs to commit %s with a different tree", input.IdempotencyKey, commit)
		}
		return gitArtifactResult(input, "commit already existed", map[string]string{"commit": commit, "tree": committedTree, "idempotencyKey": input.IdempotencyKey})
	}
	status, err := gitOutput(ctx, input.WorkspacePath, "status", "--porcelain")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if status == "" {
		head, _ := gitOutput(ctx, input.WorkspacePath, "rev-parse", "HEAD")
		return gitArtifactResult(input, "no changes to commit", map[string]string{"commit": head, "idempotencyKey": input.IdempotencyKey})
	}
	message, _ := parameter(input, "message")
	trailer := "Sovereign-Idempotency-Key: " + input.IdempotencyKey
	if _, err := runGit(ctx, input.WorkspacePath, "commit", "-m", message, "-m", trailer); err != nil {
		return utilitycontract.Result{}, err
	}
	commit, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", "HEAD")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	return gitArtifactResult(input, "commit created", map[string]string{"commit": commit, "tree": requestedTree, "idempotencyKey": input.IdempotencyKey})
}

type GitPush struct{}

func (GitPush) Name() string { return OperationGitPush }

func (GitPush) Validate(input utilitycontract.Input) error {
	if err := ensureWorkspace(input); err != nil {
		return err
	}
	if _, err := parameter(input, "branch"); err != nil {
		return err
	}
	if remote := optionalParameter(input, "remote", "origin"); remote != "origin" {
		return fmt.Errorf("git.push remote must be origin")
	}
	_, err := parameter(input, "repositoryURL")
	return err
}

func (GitPush) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	if err := verifyAdmittedRepository(ctx, input); err != nil {
		return utilitycontract.Result{}, err
	}
	branch, _ := parameter(input, "branch")
	if _, err := runGit(ctx, input.WorkspacePath, "check-ref-format", "--branch", branch); err != nil {
		return utilitycontract.Result{}, fmt.Errorf("invalid branch %q: %w", branch, err)
	}
	remote := optionalParameter(input, "remote", "origin")
	localCommit, remoteCommit, err := pushBranch(ctx, input.WorkspacePath, remote, branch)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	message := "candidate pushed"
	if remoteCommit == localCommit {
		message = "remote branch already matched candidate"
	}
	return gitArtifactResult(input, message, map[string]string{"remote": remote, "branch": branch, "commit": localCommit, "previousRemoteCommit": remoteCommit, "idempotencyKey": input.IdempotencyKey})
}

func pushBranch(ctx context.Context, workspace, remote, branch string) (localCommit, previousRemoteCommit string, err error) {
	// Observe before and after the push so a retry can distinguish "already
	// converged" from a new side effect, and an ambiguous push cannot be reported
	// successful until the remote ref resolves to the intended commit.
	localCommit, err = gitOutput(ctx, workspace, "rev-parse", branch)
	if err != nil {
		return "", "", err
	}
	remoteOutput, err := gitOutput(ctx, workspace, "ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil {
		return "", "", err
	}
	remoteCommit := ""
	if fields := strings.Fields(remoteOutput); len(fields) > 0 {
		remoteCommit = fields[0]
	}
	if remoteCommit == localCommit {
		return localCommit, remoteCommit, nil
	}
	if _, err := runGit(ctx, workspace, "push", remote, branch+":refs/heads/"+branch); err != nil {
		return "", "", err
	}
	confirmed, err := gitOutput(ctx, workspace, "ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil || !strings.HasPrefix(confirmed, localCommit) {
		return "", "", fmt.Errorf("remote branch %s/%s did not converge to %s", remote, branch, localCommit)
	}
	return localCommit, remoteCommit, nil
}

type GitMerge struct{}

func (GitMerge) Name() string {
	return OperationGitMerge
}

func (GitMerge) Validate(input utilitycontract.Input) error {
	if err := ensureWorkspace(input); err != nil {
		return err
	}
	for _, name := range []string{"sourceBranch", "candidateRevision", "approvalDecisionRef", "validationRunRef"} {
		if _, err := parameter(input, name); err != nil {
			return err
		}
	}
	if remote := optionalParameter(input, "remote", "origin"); remote != "origin" {
		return fmt.Errorf("git.merge remote must be origin")
	}
	_, err := parameter(input, "repositoryURL")
	return err
}

func (GitMerge) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	if err := verifyAdmittedRepository(ctx, input); err != nil {
		return utilitycontract.Result{}, err
	}
	source, _ := parameter(input, "sourceBranch")
	target := optionalParameter(input, "targetBranch", "main")
	for _, branch := range []string{source, target} {
		if _, err := runGit(ctx, input.WorkspacePath, "check-ref-format", "--branch", branch); err != nil {
			return utilitycontract.Result{}, fmt.Errorf("invalid branch %q: %w", branch, err)
		}
	}
	candidate, _ := parameter(input, "candidateRevision")
	// The branch name is mutable; candidateRevision is the immutable revision
	// admitted by approval and validation. Refuse to merge if they have diverged.
	actualCandidate, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", source)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if actualCandidate != candidate {
		return utilitycontract.Result{}, fmt.Errorf("source branch %q resolves to %s, not approved candidate %s", source, actualCandidate, candidate)
	}
	if target != "" {
		if _, err := runGit(ctx, input.WorkspacePath, "checkout", target); err != nil {
			return utilitycontract.Result{}, err
		}
	}
	// An ancestor check makes merge reconciliation safe after a controller or
	// runner restart: if the candidate is already present, only remote
	// convergence remains to be established.
	if _, err := runGit(ctx, input.WorkspacePath, "merge-base", "--is-ancestor", source, "HEAD"); err == nil {
		head, previousRemote, pushErr := pushBranch(ctx, input.WorkspacePath, optionalParameter(input, "remote", "origin"), target)
		if pushErr != nil {
			return utilitycontract.Result{}, pushErr
		}
		return gitArtifactResult(input, "source already merged", map[string]string{"source": source, "target": target, "commit": head, "previousRemoteCommit": previousRemote})
	}
	message := optionalParameter(input, "message", "Merge "+source)
	if !strings.Contains(message, input.IdempotencyKey) {
		message = message + "\n\nSovereign-Idempotency-Key: " + input.IdempotencyKey
	}
	if _, err := runGit(ctx, input.WorkspacePath, "merge", "--no-ff", source, "-m", message); err != nil {
		return utilitycontract.Result{}, err
	}
	commit, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", "HEAD")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	remote := optionalParameter(input, "remote", "origin")
	pushedCommit, previousRemote, err := pushBranch(ctx, input.WorkspacePath, remote, target)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if pushedCommit != commit {
		return utilitycontract.Result{}, fmt.Errorf("pushed merge revision %s does not match local merge %s", pushedCommit, commit)
	}
	return gitArtifactResult(input, "merge completed", map[string]string{
		"source": source, "target": target, "remote": remote, "candidateRevision": candidate, "commit": commit, "previousRemoteCommit": previousRemote,
		"approvalDecisionRef": input.Parameters["approvalDecisionRef"], "validationRunRef": input.Parameters["validationRunRef"],
	})
}

// gitArtifactResult turns a Git side effect into contract-bound evidence. The
// collector subsequently promotes the staged file into an immutable Artifact.
func gitArtifactResult(input utilitycontract.Input, message string, metadata map[string]string) (utilitycontract.Result, error) {
	payload := map[string]any{
		"operation":      input.Operation,
		"idempotencyKey": input.IdempotencyKey,
		"message":        message,
		"metadata":       metadata,
	}
	artifacts, err := writeOperationArtifact(input, payload)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	result := utilitycontract.Result{
		SchemaVersion: utilitycontract.Version,
		Outcome:       "Succeeded",
		Message:       message,
		Artifacts:     artifacts,
		Metadata:      metadata,
	}
	if len(metadata) == 0 {
		result.Metadata = map[string]string{"operation": input.Operation}
	}
	return result, nil
}
