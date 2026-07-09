package artifacts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
)

func TestCollectCreatesContentAddressedArtifactIdempotently(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	store := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(staging, 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(staging, "plan.md")
	if err := os.WriteFile(file, []byte("plan"), 0o640); err != nil {
		t.Fatal(err)
	}
	output := []agentcontract.ArtifactOutput{{Contract: "implementation-plan/v1", Path: file}}
	first, err := Collect(staging, store, "wf", "architect-001", "abc", output)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Collect(staging, store, "wf", "architect-001", "abc", output)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Spec.Digest != second[0].Spec.Digest {
		t.Fatalf("collection is not stable: %#v %#v", first, second)
	}
	if _, err := os.Stat(first[0].Spec.Path); err != nil {
		t.Fatal(err)
	}
	if first[0].Spec.Contract.Name != "implementation-plan" || first[0].Spec.Contract.Version != "v1" {
		t.Fatalf("unexpected contract reference: %#v", first[0].Spec.Contract)
	}
}
