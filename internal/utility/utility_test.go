package utility

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/utilitycontract"
)

func TestRepositoryBranchCommitPushAreIdempotent(t *testing.T) {
	remote, initialCommit := seedBareRepository(t)
	workspace := t.TempDir()

	initialize := utilityInput(t, workspace, "initialize", OperationRepositoryInitialize, map[string]string{
		"repositoryURL": remote, "revision": "main",
	}, "repository-revision")
	firstInitialize := runUtility(t, initialize)
	secondInitialize := runUtility(t, initialize)
	if firstInitialize.Metadata["revision"] != initialCommit || secondInitialize.Metadata["revision"] != initialCommit {
		t.Fatalf("repository initialization did not converge to %s: %#v %#v", initialCommit, firstInitialize.Metadata, secondInitialize.Metadata)
	}

	branch := utilityInput(t, workspace, "branch", OperationGitCreateBranch, map[string]string{
		"branch": "feature/control-plane", "baseRevision": initialCommit,
	}, "branch-reference")
	runUtility(t, branch)
	runUtility(t, branch)

	if err := os.WriteFile(filepath.Join(workspace, "change.txt"), []byte("controlled change\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	commit := utilityInput(t, workspace, "commit", OperationGitCommit, map[string]string{"message": "controlled change"}, "git-revision")
	firstCommit := runUtility(t, commit)
	secondCommit := runUtility(t, commit)
	if firstCommit.Metadata["commit"] == "" || firstCommit.Metadata["commit"] != secondCommit.Metadata["commit"] {
		t.Fatalf("commit retry created a different result: %#v %#v", firstCommit.Metadata, secondCommit.Metadata)
	}

	push := utilityInput(t, workspace, "push", OperationGitPush, map[string]string{
		"remote": "origin", "branch": "feature/control-plane",
	}, "pushed-revision")
	firstPush := runUtility(t, push)
	secondPush := runUtility(t, push)
	if firstPush.Metadata["commit"] != secondPush.Metadata["commit"] {
		t.Fatalf("push retry did not converge: %#v %#v", firstPush.Metadata, secondPush.Metadata)
	}
	remoteCommit := gitTestOutput(t, workspace, "ls-remote", "--heads", "origin", "refs/heads/feature/control-plane")
	if !strings.HasPrefix(remoteCommit, firstCommit.Metadata["commit"]) {
		t.Fatalf("remote ref = %q, want commit %s", remoteCommit, firstCommit.Metadata["commit"])
	}
}

func TestCommitRejectsIdempotencyKeyReuseForDifferentTree(t *testing.T) {
	remote, initialCommit := seedBareRepository(t)
	workspace := t.TempDir()
	runUtility(t, utilityInput(t, workspace, "initialize", OperationRepositoryInitialize, map[string]string{"repositoryURL": remote, "revision": initialCommit}, "repository-revision"))
	runUtility(t, utilityInput(t, workspace, "branch", OperationGitCreateBranch, map[string]string{"branch": "feature", "baseRevision": initialCommit}, "branch-reference"))
	if err := os.WriteFile(filepath.Join(workspace, "first.txt"), []byte("first"), 0o640); err != nil {
		t.Fatal(err)
	}
	input := utilityInput(t, workspace, "commit", OperationGitCommit, map[string]string{"message": "first"}, "git-revision")
	runUtility(t, input)
	if err := os.WriteFile(filepath.Join(workspace, "second.txt"), []byte("second"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := DefaultRegistry().Run(context.Background(), input); err == nil || !strings.Contains(err.Error(), "different tree") {
		t.Fatalf("expected idempotency collision, got %v", err)
	}
}

func TestGitOperationRejectsTamperedRepositoryRemote(t *testing.T) {
	remote, initialCommit := seedBareRepository(t)
	workspace := t.TempDir()
	runUtility(t, utilityInput(t, workspace, "initialize", OperationRepositoryInitialize, map[string]string{"repositoryURL": remote, "revision": initialCommit}, "repository-revision"))
	input := utilityInput(t, workspace, "branch", OperationGitCreateBranch, map[string]string{
		"branch": "feature", "baseRevision": initialCommit,
	}, "branch-reference")
	gitTestCommand(t, workspace, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unapproved.git"))
	if _, err := DefaultRegistry().Run(context.Background(), input); err == nil || !strings.Contains(err.Error(), "does not match admitted repository") {
		t.Fatalf("expected repository authority mismatch, got %v", err)
	}
}

func TestMergeRequiresAndPreservesApprovedCandidate(t *testing.T) {
	remote, initialCommit := seedBareRepository(t)
	workspace := t.TempDir()
	runUtility(t, utilityInput(t, workspace, "initialize", OperationRepositoryInitialize, map[string]string{"repositoryURL": remote, "revision": initialCommit}, "repository-revision"))
	runUtility(t, utilityInput(t, workspace, "branch", OperationGitCreateBranch, map[string]string{"branch": "feature", "baseRevision": initialCommit}, "branch-reference"))
	if err := os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("feature"), 0o640); err != nil {
		t.Fatal(err)
	}
	feature := runUtility(t, utilityInput(t, workspace, "commit", OperationGitCommit, map[string]string{"message": "feature"}, "git-revision")).Metadata["commit"]
	merge := utilityInput(t, workspace, "merge", OperationGitMerge, map[string]string{
		"sourceBranch": "feature", "targetBranch": "main", "candidateRevision": feature,
		"approvalDecisionRef": "approval-1", "validationRunRef": "validation-1",
	}, "merge-revision")
	first := runUtility(t, merge)
	second := runUtility(t, merge)
	if first.Metadata["commit"] == "" || first.Metadata["commit"] != second.Metadata["commit"] {
		t.Fatalf("merge retry created a different result: %#v %#v", first.Metadata, second.Metadata)
	}
	remoteMain := gitTestOutput(t, workspace, "ls-remote", "--heads", "origin", "refs/heads/main")
	if !strings.HasPrefix(remoteMain, first.Metadata["commit"]) {
		t.Fatalf("remote main = %q, want merge commit %s", remoteMain, first.Metadata["commit"])
	}
}

func TestProjectTemplateOperations(t *testing.T) {
	remote, initialCommit := seedBareRepository(t)
	workspace := t.TempDir()
	runUtility(t, utilityInput(t, workspace, "initialize", OperationRepositoryInitialize, map[string]string{"repositoryURL": remote, "revision": initialCommit}, "repository-revision"))

	testInput := utilityInput(t, workspace, "tests", OperationTestRun, nil, "test-report")
	testInput.Command = []string{"go", "version"}
	runUtility(t, testInput)

	digest := "sha256:" + strings.Repeat("a", 64)
	if err := os.WriteFile(filepath.Join(workspace, "image.digest"), []byte(digest), 0o640); err != nil {
		t.Fatal(err)
	}
	buildInput := utilityInput(t, workspace, "build", OperationBuildImage, map[string]string{
		"imageName": "registry.internal/sovereign/controller", "digestFile": "image.digest",
	}, "image-digest")
	buildInput.Command = []string{"go", "version"}
	result := runUtility(t, buildInput)
	repeated := runUtility(t, buildInput)
	if result.Metadata["digest"] != digest || result.Metadata["commit"] != initialCommit {
		t.Fatalf("unexpected build result: %#v", result.Metadata)
	}
	if repeated.Metadata["digest"] != result.Metadata["digest"] || repeated.Metadata["commit"] != result.Metadata["commit"] {
		t.Fatalf("build retry did not reuse the admitted result: %#v %#v", result.Metadata, repeated.Metadata)
	}
}

func utilityInput(t *testing.T, workspace, attempt, operation string, parameters map[string]string, output string) utilitycontract.Input {
	t.Helper()
	staging := filepath.Join(workspace, "attempts", attempt, "staging")
	if err := os.MkdirAll(staging, 0o750); err != nil {
		t.Fatal(err)
	}
	resolvedParameters := make(map[string]string, len(parameters)+1)
	for name, value := range parameters {
		resolvedParameters[name] = value
	}
	if strings.HasPrefix(operation, "git.") || operation == OperationBuildImage {
		resolvedParameters["repositoryURL"] = gitTestOutput(t, workspace, "remote", "get-url", "origin")
	}
	return utilitycontract.Input{
		SchemaVersion: utilitycontract.Version, WorkflowID: "wf", StepName: attempt, Attempt: 1,
		Authority:        utilitycontract.AuthorityReference{APIVersion: "aim.sovereign.io/v1alpha1", Kind: "UtilityOperation", Namespace: "wf", Name: attempt, UID: "uid-" + attempt},
		PolicyDecisionID: "test-policy",
		Operation:        operation, IdempotencyKey: "wf/" + attempt,
		Parameters: resolvedParameters, WorkspacePath: workspace, StagingPath: staging,
		Outputs: []utilitycontract.OutputObligation{{Name: output, Version: "v1", Required: true}},
	}
}

func runUtility(t *testing.T, input utilitycontract.Input) utilitycontract.Result {
	t.Helper()
	result, err := DefaultRegistry().Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func seedBareRepository(t *testing.T) (string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitTestCommand(t, "", "init", "--bare", remote)
	seed := t.TempDir()
	gitTestCommand(t, seed, "init")
	gitTestCommand(t, seed, "config", "user.name", "Test User")
	gitTestCommand(t, seed, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, seed, "add", "README.md")
	gitTestCommand(t, seed, "commit", "-m", "seed")
	gitTestCommand(t, seed, "branch", "-M", "main")
	gitTestCommand(t, seed, "remote", "add", "origin", remote)
	gitTestCommand(t, seed, "push", "-u", "origin", "main")
	return remote, gitTestOutput(t, seed, "rev-parse", "HEAD")
}

func gitTestCommand(t *testing.T, directory string, args ...string) {
	t.Helper()
	_ = gitTestOutput(t, directory, args...)
}

func gitTestOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	if directory != "" {
		command.Dir = directory
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
