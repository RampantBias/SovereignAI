package agentcontract

import (
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
