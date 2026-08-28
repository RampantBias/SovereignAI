package artifactcontract

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/SovereignAI/internal/contractschema"
)

func TestMaterializerRegistryMaterializesImplementationPlanFromCandidate(t *testing.T) {
	changeRequest := fixtureBytes(t, fixtureForContract(t, ChangeRequestContract).Document)
	repositoryRevision := fixtureBytes(t, fixtureForContract(t, RepositoryRevisionContract).Document)
	candidate := []byte(`{
		"summary":"Implement division",
		"affectedPaths":[{"path":"src/main.go","action":"modify"}],
		"implementationSteps":["Add division handling"],
		"testStrategy":["Run go test ./..."],
		"acceptanceMapping":[{"criterionIndex":1,"verification":"Exercise division"}],
		"risks":[],
		"assumptions":[]
	}`)

	registry, err := NewMaterializerRegistry()
	if err != nil {
		t.Fatal(err)
	}
	materializer, err := registry.Resolve("implementation-plan", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if materializer.Kind != MaterializationCandidate {
		t.Fatalf("materialization kind = %q, want candidate", materializer.Kind)
	}
	content, err := materializer.Materialize(MaterializationEvidence{
		Candidate: candidate,
		Sources: []SourceArtifact{
			{Contract: ChangeRequestContract, Digest: DigestBytes(changeRequest), Content: changeRequest},
			{Contract: RepositoryRevisionContract, Digest: DigestBytes(repositoryRevision), Content: repositoryRevision},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateContract(ImplementationPlanContract, content); err != nil {
		t.Fatalf("materialized plan is invalid: %v", err)
	}
	var plan ImplementationPlan
	if err := json.Unmarshal(content, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.ChangeRequestDigest != DigestBytes(changeRequest) ||
		plan.RepositoryRevisionDigest != DigestBytes(repositoryRevision) ||
		plan.SourceCommit != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("trusted provenance was not derived from sources: %#v", plan)
	}
}

func TestMaterializerRegistryAcceptsExtensionThroughArtifactcontract(t *testing.T) {
	registry, err := NewMaterializerRegistry()
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := registry.Register(NewMaterializer(
		"review-notes/v1",
		MaterializationCandidate,
		contractschema.Definition{},
		func(evidence MaterializationEvidence) ([]byte, error) {
			called = true
			return append([]byte(nil), evidence.Candidate...), nil
		},
	)); err != nil {
		t.Fatal(err)
	}
	materializer, err := registry.Resolve("review-notes", "v1")
	if err != nil {
		t.Fatal(err)
	}
	candidate := []byte(`{"summary":"reviewed"}`)
	got, err := materializer.Materialize(MaterializationEvidence{Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if !called || !bytes.Equal(got, candidate) {
		t.Fatalf("custom materializer result = %q, called=%t", got, called)
	}
}

func TestCandidateWriteUsesDigestFencing(t *testing.T) {
	root := t.TempDir()
	first, err := WriteCandidate(root, ImplementationPlanContract, []byte(`{"summary":"first"}`), CandidateDigestAbsent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCandidate(root, ImplementationPlanContract, []byte(`{"summary":"conflict"}`), CandidateDigestAbsent); err == nil {
		t.Fatal("candidate overwrite without the current digest succeeded")
	}
	secondContent := []byte(`{"summary":"second"}`)
	second, err := WriteCandidate(root, ImplementationPlanContract, secondContent, first.Digest)
	if err != nil {
		t.Fatal(err)
	}
	stored, content, err := ReadCandidate(root, ImplementationPlanContract)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Digest != second.Digest || !bytes.Equal(content, secondContent) {
		t.Fatalf("stored candidate = %#v %q, want %#v %q", stored, content, second, secondContent)
	}
}
