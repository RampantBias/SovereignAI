package artifacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
)

func TestCollectCreatesContentAddressedArtifactIdempotently(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	store := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(staging, 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(staging, "plan.json")
	if err := os.WriteFile(file, validImplementationPlan(t), 0o640); err != nil {
		t.Fatal(err)
	}
	output := []agentcontract.ArtifactOutput{{Contract: artifactcontract.ImplementationPlanContract, Path: file}}
	producer := v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: "architect-001"}
	first, err := Collect(staging, store, "wf", producer, "abc", output)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Collect(staging, store, "wf", producer, "abc", output)
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
	if first[0].Spec.ProducerRef.Kind != "AgentRun" || first[0].Spec.ProducerRef.Name != "architect-001" {
		t.Fatalf("artifact lost its authoritative producer: %#v", first[0].Spec.ProducerRef)
	}
}

func TestCollectRejectsInvalidAndUnregisteredContracts(t *testing.T) {
	staging := t.TempDir()
	store := t.TempDir()
	file := filepath.Join(staging, "artifact.json")
	if err := os.WriteFile(file, []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}
	producer := v1alpha1.TypedLocalReference{Kind: "AgentRun", Name: "architect-001"}
	for _, contract := range []string{artifactcontract.ImplementationPlanContract, "unknown/v1", "invalid/v1/extra"} {
		t.Run(contract, func(t *testing.T) {
			_, err := Collect(staging, store, "wf", producer, "abc", []agentcontract.ArtifactOutput{{Contract: contract, Path: file}})
			if err == nil {
				t.Fatal("expected collection rejection")
			}
		})
	}

	if err := os.WriteFile(file, validImplementationPlan(t), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := Collect(staging, store, "wf", producer, "abc", []agentcontract.ArtifactOutput{{
		Contract: artifactcontract.ImplementationPlanContract, Path: file, MediaType: "text/markdown",
	}})
	if err == nil || !strings.Contains(err.Error(), "requires application/json") {
		t.Fatalf("expected media-type rejection, got %v", err)
	}
}

func TestCollectRejectsTamperedExistingStoredContent(t *testing.T) {
	staging := t.TempDir()
	store := t.TempDir()
	content := validImplementationPlan(t)
	file := filepath.Join(staging, "plan.json")
	if err := os.WriteFile(file, content, 0o640); err != nil {
		t.Fatal(err)
	}
	digest := strings.TrimPrefix(artifactcontract.DigestBytes(content), "sha256:")
	if err := os.WriteFile(filepath.Join(store, digest), []byte(`{"tampered":true}`), 0o440); err != nil {
		t.Fatal(err)
	}
	producer := v1alpha1.TypedLocalReference{Kind: "AgentRun", Name: "architect-001"}
	_, err := Collect(staging, store, "wf", producer, "abc", []agentcontract.ArtifactOutput{{
		Contract: artifactcontract.ImplementationPlanContract, Path: file,
	}})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected stored digest mismatch, got %v", err)
	}
}

func validImplementationPlan(t *testing.T) []byte {
	t.Helper()
	value := artifactcontract.ImplementationPlan{
		Summary:                  "Implement division",
		ChangeRequestDigest:      "sha256:" + strings.Repeat("a", 64),
		RepositoryRevisionDigest: "sha256:" + strings.Repeat("b", 64),
		SourceCommit:             strings.Repeat("a", 40),
		AffectedPaths:            []artifactcontract.AffectedPath{{Path: "main.go", Action: "modify"}},
		ImplementationSteps:      []string{"Add divide handling"},
		TestStrategy:             []string{"Run go test ./..."},
		AcceptanceMapping:        []artifactcontract.AcceptanceMapping{{CriterionIndex: 1, Verification: "Exercise division"}},
		Risks:                    []string{},
		Assumptions:              []string{},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
