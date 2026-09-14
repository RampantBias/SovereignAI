package utility

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/utilitycontract"
	"github.com/SovereignAI/internal/workspaceeditor"
)

func recoveryCandidateFixture(t *testing.T) (utilitycontract.Input, artifactcontract.TestChangeSet) {
	t.Helper()
	remote, _ := seedBareRepository(t)
	workspace := t.TempDir()
	gitTestCommand(t, workspace, "-c", "core.autocrlf=false", "clone", remote, ".")
	gitTestCommand(t, workspace, "config", "core.autocrlf", "false")
	gitTestCommand(t, workspace, "checkout", "main")
	gitTestCommand(t, workspace, "config", "user.name", "Test")
	gitTestCommand(t, workspace, "config", "user.email", "test@example.invalid")
	original := "package calculator\n\nimport \"testing\"\nfunc TestExisting(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(workspace, "calc_test.go"), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, workspace, "add", "calc_test.go")
	gitTestCommand(t, workspace, "commit", "-m", "original tests")
	base := gitTestOutput(t, workspace, "rev-parse", "HEAD")
	gitTestCommand(t, workspace, "remote", "set-url", "origin", "https://example.invalid/calculator.git")
	if err := ensureControlPlanePathsExcluded(workspace); err != nil {
		t.Fatal(err)
	}
	request, plan := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	prior := testChangeSetForTest(base, request, plan)
	broken := strings.Replace(original, "func TestExisting(t *testing.T) {", "func TestExisting(t *testing.T) {} {", 1)
	prior.Files = []artifactcontract.ChangedFile{{Path: "calc_test.go", Action: "modify", BaseDigest: artifactcontract.DigestBytes([]byte(original)), ResultDigest: artifactcontract.DigestBytes([]byte(broken)), ResultContent: &broken}}
	input := utilityInput(t, workspace, "prepare-w000", OperationCandidatePrepare, nil, "prepared-candidate")
	addUtilityInputArtifact(t, &input, "repository-revision", artifactcontract.RepositoryRevisionContract, artifactcontract.RepositoryRevision{RepositoryURL: "https://example.invalid/calculator.git", RequestedRevision: "main", ResolvedCommit: base, UtilityOperation: artifactcontract.ObjectIdentity{Namespace: "wf", Name: "initialize", UID: "initialize-uid"}})
	addUtilityInputArtifact(t, &input, "change-set", artifactcontract.ChangeSetContract, changeSetForTest(base, request, plan))
	addUtilityInputArtifact(t, &input, "test-change-set", artifactcontract.TestChangeSetContract, prior)
	return input, prior
}

func TestCandidateRewindReplacesOnlyRecordedCandidate(t *testing.T) {
	ctx := context.Background()
	input, prior := recoveryCandidateFixture(t)
	first := runUtility(t, input)
	oldCandidate := decodeArtifact[artifactcontract.PreparedCandidate](t, first, artifactcontract.PreparedCandidateContract)
	source := workspaceeditor.SourceRoot(input.WorkspacePath, prior.BaseCommit)
	original, err := os.ReadFile(filepath.Join(source, "calc_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if artifactcontract.DigestBytes(original) != prior.Files[0].BaseDigest {
		t.Fatal("source snapshot used candidate bytes")
	}
	testInput := utilityInput(t, input.WorkspacePath, "test-w000", OperationTestRun, map[string]string{"environmentImageDigest": "sha256:" + strings.Repeat("d", 64)}, "test-report")
	testInput.Command = []string{"go", "test", "./..."}
	addResultArtifactInput(t, &testInput, "prepared-candidate", artifactcontract.PreparedCandidateContract, first)
	failed, err := DefaultRegistry().Run(ctx, testInput)
	if err != nil || failed.Error == nil || failed.Error.Code != utilitycontract.TestRunCodeError {
		t.Fatalf("expected initial candidate code failure: %#v, %v", failed, err)
	}
	repaired := string(original) + "\nfunc TestDivision(t *testing.T) { if Divide(84,2)!=42 { t.Fatal(\"divide\") } }\n"
	prior.Files[0].ResultContent = &repaired
	prior.Files[0].ResultDigest = artifactcontract.DigestBytes([]byte(repaired))
	next := utilityInput(t, input.WorkspacePath, "prepare-w001", OperationCandidatePrepare, nil, "prepared-candidate")
	next.Inputs = append(next.Inputs, input.Inputs[:2]...)
	addUtilityInputArtifact(t, &next, "test-change-set", artifactcontract.TestChangeSetContract, prior)
	second := runUtility(t, next)
	candidate := decodeArtifact[artifactcontract.PreparedCandidate](t, second, artifactcontract.PreparedCandidateContract)
	if candidate.CandidateTree == oldCandidate.CandidateTree || candidate.BaseCommit != oldCandidate.BaseCommit {
		t.Fatal("retry did not produce a fresh tree from the same base")
	}
	runUtility(t, next) // infrastructure replay remains idempotent
	passing := utilityInput(t, input.WorkspacePath, "test-w001", OperationTestRun, map[string]string{"environmentImageDigest": "sha256:" + strings.Repeat("d", 64)}, "test-report")
	passing.Command = []string{"go", "test", "./..."}
	addResultArtifactInput(t, &passing, "prepared-candidate", artifactcontract.PreparedCandidateContract, second)
	result := runUtility(t, passing)
	if result.Outcome != "Succeeded" {
		t.Fatalf("repaired candidate did not pass: %#v", result)
	}
	after, _ := os.ReadFile(filepath.Join(source, "calc_test.go"))
	if string(after) != string(original) {
		t.Fatal("source snapshot changed during recovery")
	}
}

func TestCandidateRecoveryRejectsUnrecordedEdits(t *testing.T) {
	for _, kind := range []string{"unstaged", "staged", "untracked"} {
		t.Run(kind, func(t *testing.T) {
			input, prior := recoveryCandidateFixture(t)
			runUtility(t, input)
			path := filepath.Join(input.WorkspacePath, "README.md")
			if kind == "untracked" {
				path = filepath.Join(input.WorkspacePath, "notes.txt")
			}
			if err := os.WriteFile(path, []byte("keep my changes\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			if kind == "staged" {
				gitTestCommand(t, input.WorkspacePath, "add", "README.md")
			}
			if err := restoreCandidateBase(context.Background(), input.WorkspacePath, prior.BaseCommit); err == nil {
				t.Fatal("accepted workspace drift")
			}
			data, _ := os.ReadFile(path)
			if string(data) != "keep my changes\n" {
				t.Fatal("discarded unrelated changes")
			}
		})
	}
}
