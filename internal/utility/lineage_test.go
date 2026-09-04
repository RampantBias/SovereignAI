package utility

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

func TestTypedCandidateThroughImageLineage(t *testing.T) {
	remote, baseCommit := seedBareRepository(t)
	workspace := t.TempDir()
	gitTestCommand(t, workspace, "clone", remote, ".")
	gitTestCommand(t, workspace, "checkout", "main")

	admittedURL := "https://example.invalid/calculator.git"
	fileURL := "file:///" + strings.TrimPrefix(filepath.ToSlash(remote), "/")
	gitTestCommand(t, workspace, "config", "url."+fileURL+".insteadOf", admittedURL)
	gitTestCommand(t, workspace, "remote", "set-url", "origin", admittedURL)

	changeRequestDigest := "sha256:" + strings.Repeat("a", 64)
	planDigest := "sha256:" + strings.Repeat("b", 64)
	prepare := utilityInput(t, workspace, "prepare", OperationCandidatePrepare, nil, "prepared-candidate")
	addUtilityInputArtifact(t, &prepare, "repository-revision", artifactcontract.RepositoryRevisionContract, artifactcontract.RepositoryRevision{
		RepositoryURL: admittedURL, RequestedRevision: "main", ResolvedCommit: baseCommit,
		UtilityOperation: artifactcontract.ObjectIdentity{Namespace: "wf", Name: "initialize", UID: "initialize-uid"},
	})
	addUtilityInputArtifact(t, &prepare, "test-change-set", artifactcontract.TestChangeSetContract,
		testChangeSetForTest(baseCommit, changeRequestDigest, planDigest))
	addUtilityInputArtifact(t, &prepare, "change-set", artifactcontract.ChangeSetContract,
		changeSetForTest(baseCommit, changeRequestDigest, planDigest))
	acceptedInputs := t.TempDir()
	for index := range prepare.Inputs {
		acceptedPath := filepath.Join(acceptedInputs, filepath.Base(prepare.Inputs[index].Path))
		if err := os.Rename(prepare.Inputs[index].Path, acceptedPath); err != nil {
			t.Fatal(err)
		}
		prepare.Inputs[index].Path = acceptedPath
	}
	if err := os.RemoveAll(prepare.StagingPath); err != nil {
		t.Fatal(err)
	}
	preparedResult := runUtility(t, prepare)
	if _, err := os.Stat(filepath.Join(prepare.StagingPath, "prepared-candidate.json")); err != nil {
		t.Fatalf("candidate.prepare did not create its staging directory and result: %v", err)
	}
	preparedAgain := runUtility(t, prepare)
	assertArtifactBytesEqual(t, preparedResult, preparedAgain)
	prepared := decodeArtifact[artifactcontract.PreparedCandidate](t, preparedResult, artifactcontract.PreparedCandidateContract)
	if prepared.TestChangeSetDigest != prepare.Inputs[1].Digest || prepared.ChangeSetDigest != prepare.Inputs[2].Digest {
		t.Fatalf("prepared candidate did not bind both changed-file manifests: %#v", prepared)
	}

	testInput := utilityInput(t, workspace, "test", OperationTestRun, map[string]string{
		"environmentImageDigest": "sha256:" + strings.Repeat("d", 64),
	}, "test-report")
	testInput.Command = []string{"go", "test", "./..."}
	addResultArtifactInput(t, &testInput, "prepared-candidate", artifactcontract.PreparedCandidateContract, preparedResult)
	testResult := runUtility(t, testInput)
	report := decodeArtifact[artifactcontract.TestReport](t, testResult, artifactcontract.TestReportContract)
	if report.Outcome != "passed" || report.CandidateTree != prepared.CandidateTree || report.PreparedCandidateDigest != testInput.Inputs[0].Digest {
		t.Fatalf("test report lost prepared tree lineage: %#v", report)
	}

	commitInput := utilityInput(t, workspace, "commit", OperationGitCommit, map[string]string{}, "candidate-revision")
	commitInput.Parameters["repositoryURL"] = admittedURL
	addResultArtifactInput(t, &commitInput, "prepared-candidate", artifactcontract.PreparedCandidateContract, preparedResult)
	addResultArtifactInput(t, &commitInput, "test-report", artifactcontract.TestReportContract, testResult)
	commitResult := runUtility(t, commitInput)
	candidate := decodeArtifact[artifactcontract.CandidateRevision](t, commitResult, artifactcontract.CandidateRevisionContract)
	if candidate.Tree != prepared.CandidateTree || candidate.TestReportDigest != commitInput.Inputs[1].Digest || candidate.Branch != prepared.Branch {
		t.Fatalf("candidate revision lost tested tree lineage: %#v", candidate)
	}

	pushInput := utilityInput(t, workspace, "push", OperationGitPush, map[string]string{}, "candidate-remote-proof")
	pushInput.Parameters["repositoryURL"] = admittedURL
	addResultArtifactInput(t, &pushInput, "candidate-revision", artifactcontract.CandidateRevisionContract, commitResult)
	pushResult := runUtility(t, pushInput)
	proof := decodeArtifact[artifactcontract.CandidateRemoteProof](t, pushResult, artifactcontract.CandidateRemoteProofContract)
	if proof.ObservedCommit != candidate.Commit || proof.CandidateRevisionDigest != pushInput.Inputs[0].Digest {
		t.Fatalf("remote proof lost candidate lineage: %#v", proof)
	}

	digest := "sha256:" + strings.Repeat("c", 64)
	if err := os.WriteFile(filepath.Join(workspace, "image.digest"), []byte(digest), 0o640); err != nil {
		t.Fatal(err)
	}
	buildInput := utilityInput(t, workspace, "build", OperationBuildImage, map[string]string{
		"imageName": "registry.internal/sovereign/calculator", "digestFile": "image.digest",
		"builderImageDigest": "sha256:" + strings.Repeat("e", 64),
	}, "image-digest")
	buildInput.Parameters["repositoryURL"] = admittedURL
	buildInput.Command = []string{"go", "version"}
	addResultArtifactInput(t, &buildInput, "candidate-revision", artifactcontract.CandidateRevisionContract, commitResult)
	addResultArtifactInput(t, &buildInput, "candidate-remote-proof", artifactcontract.CandidateRemoteProofContract, pushResult)
	buildResult := runUtility(t, buildInput)
	buildAgain := runUtility(t, buildInput)
	assertArtifactBytesEqual(t, buildResult, buildAgain)
	image := decodeArtifact[artifactcontract.ImageDigest](t, buildResult, artifactcontract.ImageDigestContract)
	if image.CandidateRevisionDigest != buildInput.Inputs[0].Digest || image.CandidateCommit != candidate.Commit ||
		image.CandidateTree != candidate.Tree || image.ContextTree != candidate.Tree || image.Digest != digest {
		t.Fatalf("image digest lost candidate lineage: %#v", image)
	}
}

func TestCandidateRevisionStateReportsFailedTestOutcome(t *testing.T) {
	prepared := artifactcontract.PreparedCandidate{Branch: "sovereign/candidate", CandidateTree: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	report := artifactcontract.TestReport{CandidateTree: prepared.CandidateTree, Outcome: "failed"}
	err := validateCandidateRevisionState(prepared, report, prepared.Branch, prepared.CandidateTree)
	if err == nil || !strings.Contains(err.Error(), `test-report outcome "failed"`) {
		t.Fatalf("failed test outcome returned %v", err)
	}
}

func testChangeSetForTest(base, changeRequestDigest, planDigest string) artifactcontract.TestChangeSet {
	content := "package calculator\n\nimport \"testing\"\n\nfunc TestDivide(t *testing.T) {\n\tif Divide(84, 2) != 42 { t.Fatal(\"divide failed\") }\n}\n"
	return artifactcontract.TestChangeSet{
		Summary: "add divide test", BaseCommit: base,
		ChangeRequestDigest: changeRequestDigest, ImplementationPlanDigest: planDigest,
		Files: []artifactcontract.ChangedFile{addedFile("calc_test.go", content)},
	}
}

func changeSetForTest(base, changeRequestDigest, planDigest string) artifactcontract.ChangeSet {
	dockerfile := "FROM scratch\nCOPY calc /calc\n"
	calculator := "package calculator\n\nfunc Divide(a, b int) int { return a / b }\n"
	module := "module example.invalid/calculator\n\ngo 1.26.2\n"
	return artifactcontract.ChangeSet{
		Summary: "implement divide", BaseCommit: base,
		ChangeRequestDigest: changeRequestDigest, ImplementationPlanDigest: planDigest,
		Files: []artifactcontract.ChangedFile{
			addedFile("Dockerfile", dockerfile),
			addedFile("calc.go", calculator),
			addedFile("go.mod", module),
		},
	}
}

func addedFile(path, content string) artifactcontract.ChangedFile {
	return artifactcontract.ChangedFile{
		Path: path, Action: "add", BaseDigest: "absent",
		ResultDigest: artifactcontract.DigestBytes([]byte(content)), ResultContent: &content,
	}
}

func addResultArtifactInput(t *testing.T, input *utilitycontract.Input, name, contract string, result utilitycontract.Result) {
	t.Helper()
	data, err := os.ReadFile(result.Artifacts[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	input.Inputs = append(input.Inputs, utilitycontract.ArtifactInput{Name: name, Contract: contract,
		Digest: artifactcontract.DigestBytes(data), Path: result.Artifacts[0].Path})
}

func decodeArtifact[T any](t *testing.T, result utilitycontract.Result, contract string) T {
	t.Helper()
	var value T
	data, err := os.ReadFile(result.Artifacts[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifactcontract.ValidateContract(contract, data); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertArtifactBytesEqual(t *testing.T, first, second utilitycontract.Result) {
	t.Helper()
	firstBytes, err := os.ReadFile(first.Artifacts[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second.Artifacts[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatal("idempotent retry changed typed artifact bytes")
	}
}
