package agentcontract

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResultRejectsArtifactPathEscape(t *testing.T) {
	root := t.TempDir()
	result := Result{
		SchemaVersion: Version,
		Outcome:       "Succeeded",
		Artifacts:     []ArtifactOutput{{Contract: "patch/v1", Path: filepath.Join(root, "..", "secret")}},
	}
	if err := result.Validate(root); err == nil {
		t.Fatal("expected path escape to be rejected")
	}
}

func TestWriteAndReadResult(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "patch.diff")
	resultPath := filepath.Join(root, "control", "result.json")
	want := Result{SchemaVersion: Version, Outcome: "Succeeded", Artifacts: []ArtifactOutput{{Contract: "patch/v1", Path: artifact}}}
	if err := WriteResult(resultPath, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadResult(resultPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != want.Outcome || len(got.Artifacts) != 1 {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestResultMustSatisfyRequiredOutputObligations(t *testing.T) {
	root := t.TempDir()
	input := Input{
		SchemaVersion: Version,
		WorkflowID:    "wf",
		StepName:      "architect",
		Attempt:       1,
		Role:          "architect",
		Goal:          "plan",
		WorkspacePath: root,
		StagingPath:   root,
		Outputs: []OutputObligation{{
			Name:     "implementation-plan",
			Version:  "v1",
			Required: true,
		}},
	}
	result := Result{
		SchemaVersion: Version,
		Outcome:       "Succeeded",
	}
	if err := result.ValidateAgainst(input); err == nil {
		t.Fatal("expected missing required output to be rejected")
	}
}

func TestResultRejectsUndeclaredArtifactsWhenOutputsAreDeclared(t *testing.T) {
	root := t.TempDir()
	input := Input{
		SchemaVersion: Version,
		WorkflowID:    "wf",
		StepName:      "architect",
		Attempt:       1,
		Role:          "architect",
		Goal:          "plan",
		WorkspacePath: root,
		StagingPath:   root,
		Outputs: []OutputObligation{{
			Name:     "implementation-plan",
			Version:  "v1",
			Required: true,
		}},
	}
	result := Result{
		SchemaVersion: Version,
		Outcome:       "Succeeded",
		Artifacts:     []ArtifactOutput{{Contract: "review-report/v1", Path: filepath.Join(root, "review.md")}},
	}
	if err := result.ValidateAgainst(input); err == nil {
		t.Fatal("expected undeclared artifact to be rejected")
	}
}

func TestResultSatisfiesDeclaredOutput(t *testing.T) {
	root := t.TempDir()
	input := Input{
		SchemaVersion: Version,
		WorkflowID:    "wf",
		StepName:      "architect",
		Attempt:       1,
		Role:          "architect",
		Goal:          "plan",
		WorkspacePath: root,
		StagingPath:   root,
		Outputs: []OutputObligation{{
			Name:      "implementation-plan",
			Version:   "v1",
			Required:  true,
			MediaType: "text/markdown",
		}},
	}
	result := Result{
		SchemaVersion: Version,
		Outcome:       "Succeeded",
		Artifacts:     []ArtifactOutput{{Contract: "implementation-plan/v1", Path: filepath.Join(root, "plan.md"), MediaType: "text/markdown"}},
	}
	if err := result.ValidateAgainst(input); err != nil {
		t.Fatal(err)
	}
}

func TestReadResultForInputRejectsMissingArtifactFile(t *testing.T) {
	root := t.TempDir()
	input := inputWithRequiredOutput(root)
	resultPath := filepath.Join(root, "control", "result.json")
	result := Result{
		SchemaVersion: Version,
		Outcome:       "Succeeded",
		Artifacts:     []ArtifactOutput{{Contract: "implementation-plan/v1", Path: filepath.Join(root, "plan.md")}},
	}
	if err := WriteResult(resultPath, result); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResultForInput(resultPath, input); err == nil {
		t.Fatal("expected missing artifact file to be rejected")
	}
}

func TestReadResultForInputRejectsDirectoryArtifact(t *testing.T) {
	root := t.TempDir()
	input := inputWithRequiredOutput(root)
	artifact := filepath.Join(root, "plan.md")
	if err := os.MkdirAll(artifact, 0o750); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(root, "control", "result.json")
	result := Result{
		SchemaVersion: Version,
		Outcome:       "Succeeded",
		Artifacts:     []ArtifactOutput{{Contract: "implementation-plan/v1", Path: artifact}},
	}
	if err := WriteResult(resultPath, result); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResultForInput(resultPath, input); err == nil {
		t.Fatal("expected directory artifact to be rejected")
	}
}

func TestReadResultForInputAcceptsRegularDeclaredArtifact(t *testing.T) {
	root := t.TempDir()
	input := inputWithRequiredOutput(root)
	artifact := filepath.Join(root, "plan.md")
	if err := os.WriteFile(artifact, []byte("plan"), 0o640); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(root, "control", "result.json")
	result := Result{
		SchemaVersion: Version,
		Outcome:       "Succeeded",
		Artifacts:     []ArtifactOutput{{Contract: "implementation-plan/v1", Path: artifact}},
	}
	if err := WriteResult(resultPath, result); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResultForInput(resultPath, input); err != nil {
		t.Fatal(err)
	}
}

func inputWithRequiredOutput(root string) Input {
	return Input{
		SchemaVersion: Version,
		WorkflowID:    "wf",
		StepName:      "architect",
		Attempt:       1,
		Role:          "architect",
		Goal:          "plan",
		WorkspacePath: root,
		StagingPath:   root,
		Outputs: []OutputObligation{{
			Name:     "implementation-plan",
			Version:  "v1",
			Required: true,
		}},
	}
}
