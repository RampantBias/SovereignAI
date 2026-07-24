package utilitycontract

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResultValidatesRequiredOutputObligations(t *testing.T) {
	staging := t.TempDir()
	artifactPath := filepath.Join(staging, "report.json")
	if err := os.WriteFile(artifactPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := Input{
		SchemaVersion:    Version,
		WorkflowID:       "wf",
		StepName:         "tests",
		Attempt:          1,
		Authority:        AuthorityReference{APIVersion: "aim.sovereign.io/v1alpha1", Kind: "UtilityOperation", Namespace: "wf", Name: "tests-001", UID: "uid-tests-001"},
		PolicyDecisionID: "policy-test",
		Operation:        "test.run",
		Command:          []string{"go", "test", "./..."},
		IdempotencyKey:   "ns/tests-001",
		WorkspacePath:    t.TempDir(),
		StagingPath:      staging,
		WorkspaceWrite:   WorkspaceWriteAuthority{LeaseName: "wf-workspace-writer", HolderIdentity: "UtilityOperation/wf/tests-001/uid-tests-001", WriterEpoch: 1},
		Outputs:          []OutputObligation{{Name: "test-report", Version: "v1", Required: true}},
	}
	result := Result{
		SchemaVersion: Version,
		Outcome:       "Succeeded",
		Artifacts:     []ArtifactOutput{{Contract: "test-report/v1", Path: artifactPath}},
	}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(staging); err != nil {
		t.Fatal(err)
	}
	if err := result.ValidateAgainst(input); err != nil {
		t.Fatal(err)
	}
	if err := result.ValidateArtifactFiles(staging); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactPathRejectsEscapes(t *testing.T) {
	staging := t.TempDir()
	escape := filepath.Join(staging, "..", "outside.txt")
	if _, err := ArtifactPath(staging, escape); err == nil {
		t.Fatal("expected escaped artifact path to be rejected")
	}
}
