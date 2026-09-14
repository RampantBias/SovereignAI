package utility

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

type GitCreateBranch struct{}

func (GitCreateBranch) Name() string {
	return OperationGitCreateBranch
}

func (GitCreateBranch) Validate(input utilitycontract.Input) error {
	if err := EnsureWorkspace(input); err != nil {
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
	baseRevision := OptionalParameter(input, "baseRevision", "HEAD")
	baseCommit, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", baseRevision+"^{commit}")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	message := "branch created"
	if _, err := runGit(ctx, input.WorkspacePath, "rev-parse", "--verify", branch); err == nil {
		if _, err := runGit(ctx, input.WorkspacePath, "merge-base", "--is-ancestor", baseRevision, branch); err != nil {
			return utilitycontract.Result{}, fmt.Errorf("existing branch %q is not based on %q", branch, baseRevision)
		}
		if _, err := runGit(ctx, input.WorkspacePath, "checkout", branch); err != nil {
			return utilitycontract.Result{}, err
		}
		message = "branch already existed"
	} else {
		if _, err := runGit(ctx, input.WorkspacePath, "checkout", "-b", branch, baseRevision); err != nil {
			return utilitycontract.Result{}, err
		}
	}
	commit, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", branch+"^{commit}")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	metadata := map[string]string{"branch": branch, "baseRevision": baseRevision, "commit": commit}
	return GitArtifactResult(input, message, metadata, artifactcontract.BranchReference{
		RepositoryURL:    input.Parameters["repositoryURL"],
		Branch:           branch,
		BaseCommit:       baseCommit,
		Commit:           commit,
		UtilityOperation: artifactcontract.ObjectIdentity{Namespace: input.Authority.Namespace, Name: input.Authority.Name, UID: input.Authority.UID},
		IdempotencyKey:   input.IdempotencyKey,
	})
}

type GitCommit struct{}

func (GitCommit) Name() string {
	return OperationGitCommit
}

func (GitCommit) Validate(input utilitycontract.Input) error {
	if err := EnsureWorkspace(input); err != nil {
		return err
	}
	if _, err := parameter(input, "repositoryURL"); err != nil {
		return err
	}
	if OutputContract(input) == artifactcontract.CandidateRevisionContract {
		if _, err := RequiredInput(input, artifactcontract.PreparedCandidateContract); err != nil {
			return err
		}
		_, err := RequiredInput(input, artifactcontract.TestReportContract)
		return err
	}
	_, err := parameter(input, "message")
	return err
}

func (GitCommit) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	if err := verifyAdmittedRepository(ctx, input); err != nil {
		return utilitycontract.Result{}, err
	}
	if OutputContract(input) != artifactcontract.CandidateRevisionContract {
		if _, err := runGit(ctx, input.WorkspacePath, "add", "-A"); err != nil {
			return utilitycontract.Result{}, err
		}
	}
	// write-tree records the exact staged content without creating a commit. It
	// lets a retry prove that an existing idempotent commit represents the same
	// requested effect before that commit is re-used.
	requestedTree, err := gitOutput(ctx, input.WorkspacePath, "write-tree")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if err := validateCommitInputs(ctx, input, requestedTree); err != nil {
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
		return gitCommitResult(ctx, input, "commit already existed", commit, committedTree)
	}
	status, err := gitOutput(ctx, input.WorkspacePath, "status", "--porcelain")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if status == "" {
		head, _ := gitOutput(ctx, input.WorkspacePath, "rev-parse", "HEAD")
		return gitCommitResult(ctx, input, "no changes to commit", head, requestedTree)
	}
	message, err := admittedCommitMessage(input)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	trailer := "Sovereign-Idempotency-Key: " + input.IdempotencyKey
	if _, err := runGit(ctx, input.WorkspacePath, "commit", "-m", message, "-m", trailer); err != nil {
		return utilitycontract.Result{}, err
	}
	commit, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", "HEAD")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	return gitCommitResult(ctx, input, "commit created", commit, requestedTree)
}

type GitPush struct{}

func (GitPush) Name() string { return OperationGitPush }

func (GitPush) Validate(input utilitycontract.Input) error {
	if err := EnsureWorkspace(input); err != nil {
		return err
	}
	if remote := OptionalParameter(input, "remote", "origin"); remote != "origin" {
		return fmt.Errorf("git.push remote must be origin")
	}
	if _, err := parameter(input, "repositoryURL"); err != nil {
		return err
	}
	if OutputContract(input) == artifactcontract.CandidateRemoteProofContract {
		_, err := RequiredInput(input, artifactcontract.CandidateRevisionContract)
		return err
	}
	_, err := parameter(input, "branch")
	return err
}

func (GitPush) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	if err := verifyAdmittedRepository(ctx, input); err != nil {
		return utilitycontract.Result{}, err
	}
	var candidateRef utilitycontract.ArtifactInput
	branch := ""
	if OutputContract(input) == artifactcontract.CandidateRemoteProofContract {
		reference, candidate, err := ReadInputArtifact[artifactcontract.CandidateRevision](input, artifactcontract.CandidateRevisionContract)
		if err != nil {
			return utilitycontract.Result{}, err
		}
		candidateRef = reference
		branch = candidate.Branch
		localCommit, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", branch)
		if err != nil {
			return utilitycontract.Result{}, err
		}
		if candidate.Commit != localCommit {
			return utilitycontract.Result{}, fmt.Errorf("candidate-revision input does not identify branch %s at %s", branch, localCommit)
		}
	} else {
		var err error
		branch, err = parameter(input, "branch")
		if err != nil {
			return utilitycontract.Result{}, err
		}
	}
	if _, err := runGit(ctx, input.WorkspacePath, "check-ref-format", "--branch", branch); err != nil {
		return utilitycontract.Result{}, fmt.Errorf("invalid branch %q: %w", branch, err)
	}
	remote := OptionalParameter(input, "remote", "origin")
	localCommit, remoteCommit, err := pushBranch(ctx, input.WorkspacePath, remote, branch)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	message := "candidate pushed"
	if remoteCommit == localCommit {
		message = "remote branch already matched candidate"
	}
	metadata := map[string]string{"remote": remote, "branch": branch, "commit": localCommit, "previousRemoteCommit": remoteCommit, "idempotencyKey": input.IdempotencyKey}
	payload := any(metadata)
	if OutputContract(input) == artifactcontract.CandidateRemoteProofContract {
		payload = artifactcontract.CandidateRemoteProof{
			CandidateRevisionDigest: candidateRef.Digest, RepositoryURL: input.Parameters["repositoryURL"],
			Ref: "refs/heads/" + branch, ObservedCommit: localCommit, VerifiedAt: proofTimestamp(input, "refs/heads/"+branch, localCommit),
		}
	}
	return GitArtifactResult(input, message, metadata, payload)
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
	if err := EnsureWorkspace(input); err != nil {
		return err
	}
	for _, name := range []string{"sourceBranch", "candidateRevision", "approvalDecisionRef", "validationRunRef"} {
		if _, err := parameter(input, name); err != nil {
			return err
		}
	}
	if remote := OptionalParameter(input, "remote", "origin"); remote != "origin" {
		return fmt.Errorf("git.merge remote must be origin")
	}
	_, err := parameter(input, "repositoryURL")
	if err == nil && OutputContract(input) == artifactcontract.MergeRevisionContract {
		for _, contract := range []string{artifactcontract.CandidateRemoteProofContract, artifactcontract.CandidateRevisionContract, artifactcontract.ValidationResultContract} {
			if _, err = RequiredInput(input, contract); err != nil {
				return err
			}
		}
		for _, name := range []string{"approvalRequestRef", "approvalRequestUID", "approvalDecisionUID"} {
			if _, err = parameter(input, name); err != nil {
				return err
			}
		}
	}
	return err
}

func (GitMerge) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	if err := verifyAdmittedRepository(ctx, input); err != nil {
		return utilitycontract.Result{}, err
	}
	source, _ := parameter(input, "sourceBranch")
	target := OptionalParameter(input, "targetBranch", "main")
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
	if err := validateMergeInputs(input, source, candidate); err != nil {
		return utilitycontract.Result{}, err
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
		head, previousRemote, pushErr := pushBranch(ctx, input.WorkspacePath, OptionalParameter(input, "remote", "origin"), target)
		if pushErr != nil {
			return utilitycontract.Result{}, pushErr
		}
		return gitMergeResult(ctx, input, "source already merged", source, target, head, previousRemote)
	}
	message := OptionalParameter(input, "message", "Merge "+source)
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
	remote := OptionalParameter(input, "remote", "origin")
	pushedCommit, previousRemote, err := pushBranch(ctx, input.WorkspacePath, remote, target)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if pushedCommit != commit {
		return utilitycontract.Result{}, fmt.Errorf("pushed merge revision %s does not match local merge %s", pushedCommit, commit)
	}
	metadata := map[string]string{
		"source": source, "target": target, "remote": remote, "candidateRevision": candidate, "commit": commit, "previousRemoteCommit": previousRemote,
		"approvalDecisionRef": input.Parameters["approvalDecisionRef"], "validationRunRef": input.Parameters["validationRunRef"],
	}
	return gitMergeResult(ctx, input, "merge completed", source, target, commit, previousRemote, metadata)
}

func admittedCommitMessage(input utilitycontract.Input) (string, error) {
	if OutputContract(input) != artifactcontract.CandidateRevisionContract {
		return parameter(input, "message")
	}
	preparedRef, _, err := ReadInputArtifact[artifactcontract.PreparedCandidate](input, artifactcontract.PreparedCandidateContract)
	if err != nil {
		return "", err
	}
	return "SovereignAI candidate " + sha256String(preparedRef.Digest)[:12], nil
}

func gitCommitResult(ctx context.Context, input utilitycontract.Input, message, commit, tree string) (utilitycontract.Result, error) {
	metadata := map[string]string{"commit": commit, "tree": tree, "idempotencyKey": input.IdempotencyKey}
	payload := any(metadata)
	if OutputContract(input) == artifactcontract.CandidateRevisionContract {
		preparedRef, prepared, err := ReadInputArtifact[artifactcontract.PreparedCandidate](input, artifactcontract.PreparedCandidateContract)
		if err != nil {
			return utilitycontract.Result{}, err
		}
		testRef, report, err := ReadInputArtifact[artifactcontract.TestReport](input, artifactcontract.TestReportContract)
		if err != nil {
			return utilitycontract.Result{}, err
		}
		branch, err := gitOutput(ctx, input.WorkspacePath, "branch", "--show-current")
		if err != nil {
			return utilitycontract.Result{}, err
		}
		if err := validateCandidateRevisionState(prepared, report, branch, tree); err != nil {
			return utilitycontract.Result{}, err
		}
		commitMessage, err := gitOutput(ctx, input.WorkspacePath, "show", "-s", "--format=%B", commit)
		if err != nil {
			return utilitycontract.Result{}, err
		}
		payload = artifactcontract.CandidateRevision{
			RepositoryURL: input.Parameters["repositoryURL"], PreparedCandidateDigest: preparedRef.Digest,
			TestReportDigest: testRef.Digest, ChangeSetDigest: prepared.ChangeSetDigest, SourceCommit: prepared.BaseCommit,
			Branch: branch, Commit: commit, Tree: tree, CommitMessageDigest: artifactcontract.DigestBytes([]byte(commitMessage)),
		}
	}
	return GitArtifactResult(input, message, metadata, payload)
}

func validateCommitInputs(ctx context.Context, input utilitycontract.Input, tree string) error {
	if OutputContract(input) != artifactcontract.CandidateRevisionContract {
		return nil
	}
	_, prepared, err := ReadInputArtifact[artifactcontract.PreparedCandidate](input, artifactcontract.PreparedCandidateContract)
	if err != nil {
		return err
	}
	_, report, err := ReadInputArtifact[artifactcontract.TestReport](input, artifactcontract.TestReportContract)
	if err != nil {
		return err
	}
	branch, err := gitOutput(ctx, input.WorkspacePath, "branch", "--show-current")
	if err != nil {
		return err
	}
	return validateCandidateRevisionState(prepared, report, branch, tree)
}

func validateCandidateRevisionState(prepared artifactcontract.PreparedCandidate, report artifactcontract.TestReport, branch, tree string) error {
	if prepared.Branch != branch {
		return fmt.Errorf("prepared-candidate branch %q does not match current branch %q", prepared.Branch, branch)
	}
	if prepared.CandidateTree != tree {
		return fmt.Errorf("prepared-candidate tree %s does not match current tree %s", prepared.CandidateTree, tree)
	}
	if report.CandidateTree != tree {
		return fmt.Errorf("test-report candidate tree %s does not match current tree %s", report.CandidateTree, tree)
	}
	if report.Outcome != "passed" {
		return fmt.Errorf("test-report outcome %q does not authorize candidate commit; expected %q", report.Outcome, "passed")
	}
	return nil
}

func validateMergeInputs(input utilitycontract.Input, source, candidateCommit string) error {
	if OutputContract(input) != artifactcontract.MergeRevisionContract {
		return nil
	}
	candidateRef, candidate, err := ReadInputArtifact[artifactcontract.CandidateRevision](input, artifactcontract.CandidateRevisionContract)
	if err != nil {
		return err
	}
	_, proof, err := ReadInputArtifact[artifactcontract.CandidateRemoteProof](input, artifactcontract.CandidateRemoteProofContract)
	if err != nil {
		return err
	}
	_, validation, err := ReadInputArtifact[artifactcontract.ValidationResult](input, artifactcontract.ValidationResultContract)
	if err != nil {
		return err
	}
	if candidate.Branch != source || candidate.Commit != candidateCommit || proof.CandidateRevisionDigest != candidateRef.Digest ||
		proof.ObservedCommit != candidateCommit || validation.CandidateRevisionDigest != candidateRef.Digest || validation.Outcome != "passed" {
		return fmt.Errorf("merge inputs do not identify one pushed and validated candidate revision")
	}
	return nil
}

func gitMergeResult(ctx context.Context, input utilitycontract.Input, message, source, target, commit, previousRemote string, extra ...map[string]string) (utilitycontract.Result, error) {
	metadata := map[string]string{"source": source, "target": target, "commit": commit, "previousRemoteCommit": previousRemote}
	if len(extra) > 0 {
		metadata = extra[0]
	}
	payload := any(metadata)
	if OutputContract(input) == artifactcontract.MergeRevisionContract {
		candidateRef, candidate, err := ReadInputArtifact[artifactcontract.CandidateRevision](input, artifactcontract.CandidateRevisionContract)
		if err != nil {
			return utilitycontract.Result{}, err
		}
		validationRef, validation, err := ReadInputArtifact[artifactcontract.ValidationResult](input, artifactcontract.ValidationResultContract)
		if err != nil {
			return utilitycontract.Result{}, err
		}
		_, remoteProof, err := ReadInputArtifact[artifactcontract.CandidateRemoteProof](input, artifactcontract.CandidateRemoteProofContract)
		if err != nil {
			return utilitycontract.Result{}, err
		}
		if candidate.Commit != remoteProof.ObservedCommit || remoteProof.CandidateRevisionDigest != candidateRef.Digest || candidate.Commit != input.Parameters["candidateRevision"] {
			return utilitycontract.Result{}, fmt.Errorf("merge inputs do not identify the admitted candidate revision")
		}
		candidateTree, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", candidate.Commit+"^{tree}")
		if err != nil {
			return utilitycontract.Result{}, err
		}
		mergeTree, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", commit+"^{tree}")
		if err != nil {
			return utilitycontract.Result{}, err
		}
		previousTarget, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", commit+"^1")
		if err != nil {
			return utilitycontract.Result{}, fmt.Errorf("merge revision %s has no first parent: %w", commit, err)
		}
		payload = artifactcontract.MergeRevision{
			CandidateRevisionDigest: candidateRef.Digest, ValidationResultDigest: validationRef.Digest,
			RepositoryURL: input.Parameters["repositoryURL"], TargetBranch: target, PreviousTargetCommit: previousTarget,
			CandidateCommit: candidate.Commit, CandidateTree: candidateTree, MergeCommit: commit, MergeTree: mergeTree,
			ApprovalRequest:  objectIdentity(input, "approvalRequestRef", "approvalRequestUID"),
			ApprovalDecision: objectIdentity(input, "approvalDecisionRef", "approvalDecisionUID"), ValidationRun: validation.ValidationRun,
			RemoteProof: artifactcontract.RemoteProof{Ref: "refs/heads/" + target, ObservedCommit: commit, VerifiedAt: proofTimestamp(input, "refs/heads/"+target, commit)},
		}
	}
	return GitArtifactResult(input, message, metadata, payload)
}

func ReadInputArtifact[T any](input utilitycontract.Input, contract string) (utilitycontract.ArtifactInput, T, error) {
	var value T
	reference, err := RequiredInput(input, contract)
	if err != nil {
		return reference, value, err
	}
	data, err := os.ReadFile(reference.Path)
	if err != nil {
		return reference, value, fmt.Errorf("read %s input: %w", contract, err)
	}
	if artifactcontract.DigestBytes(data) != reference.Digest {
		return reference, value, fmt.Errorf("%s input digest does not match its content", contract)
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return reference, value, fmt.Errorf("decode %s input: %w", contract, err)
	}
	return reference, value, nil
}

func RequiredInput(input utilitycontract.Input, contract string) (utilitycontract.ArtifactInput, error) {
	for _, reference := range input.Inputs {
		if reference.Contract == contract {
			if reference.Digest == "" || reference.Path == "" {
				return reference, fmt.Errorf("%s input requires digest and path", contract)
			}
			return reference, nil
		}
	}
	return utilitycontract.ArtifactInput{}, fmt.Errorf("%s input is required", contract)
}

func OutputContract(input utilitycontract.Input) string {
	if len(input.Outputs) == 0 {
		return ""
	}
	return input.Outputs[0].Contract()
}

func objectIdentity(input utilitycontract.Input, nameParameter, uidParameter string) artifactcontract.ObjectIdentity {
	return artifactcontract.ObjectIdentity{Namespace: input.Authority.Namespace, Name: input.Parameters[nameParameter], UID: input.Parameters[uidParameter]}
}

func nowTimestamp() string { return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339) }

func proofTimestamp(input utilitycontract.Input, ref, commit string) string {
	if len(input.Outputs) > 0 {
		path := filepath.Join(input.StagingPath, strings.ReplaceAll(input.Outputs[0].Name, "/", "-")+".json")
		if data, err := os.ReadFile(path); err == nil {
			if OutputContract(input) == artifactcontract.CandidateRemoteProofContract {
				var proof artifactcontract.CandidateRemoteProof
				if json.Unmarshal(data, &proof) == nil && proof.Ref == ref && proof.ObservedCommit == commit && proof.VerifiedAt != "" {
					return proof.VerifiedAt
				}
			} else if OutputContract(input) == artifactcontract.MergeRevisionContract {
				var merge artifactcontract.MergeRevision
				if json.Unmarshal(data, &merge) == nil && merge.RemoteProof.Ref == ref && merge.RemoteProof.ObservedCommit == commit && merge.RemoteProof.VerifiedAt != "" {
					return merge.RemoteProof.VerifiedAt
				}
			}
		}
	}
	return nowTimestamp()
}

// GitArtifactResult turns a Git side effect into contract-bound evidence. The
// collector subsequently promotes the staged file into an immutable Artifact.
func GitArtifactResult(input utilitycontract.Input, message string, metadata map[string]string, payload any) (utilitycontract.Result, error) {
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
