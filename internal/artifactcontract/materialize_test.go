package artifactcontract

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
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

func TestWorkspaceMaterializerCapturesCompleteChangedFiles(t *testing.T) {
	changeRequest := fixtureBytes(t, fixtureForContract(t, ChangeRequestContract).Document)
	repositoryRevision := fixtureBytes(t, fixtureForContract(t, RepositoryRevisionContract).Document)
	plan := fixtureBytes(t, fixtureForContract(t, ImplementationPlanContract).Document)
	resultContent := "package main\n\nfunc Divide(a, b int) int { return a / b }\n"
	files := []ChangedFile{{
		Path: "main.go", Action: "modify",
		BaseDigest:   DigestBytes([]byte("package main\n")),
		ResultDigest: DigestBytes([]byte(resultContent)), ResultContent: &resultContent,
	}}
	registry, err := NewMaterializerRegistry()
	if err != nil {
		t.Fatal(err)
	}
	materializer, err := registry.Resolve("change-set", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if materializer.Kind != MaterializationWorkspace || len(materializer.CandidateSchema.JSON) != 0 {
		t.Fatalf("workspace materializer exposes a generation candidate schema: %#v", materializer)
	}
	content, err := materializer.Materialize(MaterializationEvidence{
		Summary: "implement divide", Files: files,
		Sources: []SourceArtifact{
			{Contract: ChangeRequestContract, Digest: DigestBytes(changeRequest), Content: changeRequest},
			{Contract: RepositoryRevisionContract, Digest: DigestBytes(repositoryRevision), Content: repositoryRevision},
			{Contract: ImplementationPlanContract, Digest: DigestBytes(plan), Content: plan},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var changeSet ChangeSet
	if err := json.Unmarshal(content, &changeSet); err != nil {
		t.Fatal(err)
	}
	if changeSet.BaseCommit != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		changeSet.ChangeRequestDigest != DigestBytes(changeRequest) || changeSet.ImplementationPlanDigest != DigestBytes(plan) ||
		len(changeSet.Files) != 1 || changeSet.Files[0].ResultContent == nil || *changeSet.Files[0].ResultContent != resultContent {
		t.Fatalf("materialized change set lost content or provenance: %#v", changeSet)
	}
}
func TestImplementationPlanAffectedPathsAreGroundedInWorkspaceTree(t *testing.T) {
	changeRequest := fixtureBytes(t, fixtureForContract(t, ChangeRequestContract).Document)
	repositoryRevision := fixtureBytes(t, fixtureForContract(t, RepositoryRevisionContract).Document)
	sources := []SourceArtifact{
		{Contract: ChangeRequestContract, Digest: DigestBytes(changeRequest), Content: changeRequest},
		{Contract: RepositoryRevisionContract, Digest: DigestBytes(repositoryRevision), Content: repositoryRevision},
	}
	registry, err := NewMaterializerRegistry()
	if err != nil {
		t.Fatal(err)
	}
	materializer, err := registry.Resolve("implementation-plan", "v1")
	if err != nil {
		t.Fatal(err)
	}
	candidate := []byte(`{
		"summary":"Implement division",
		"affectedPaths":[{"path":"src/main.go","action":"modify"}],
		"implementationSteps":["Add division handling"],
		"testStrategy":["Run tests"],
		"acceptanceMapping":[{"criterionIndex":1,"verification":"Exercise division"}],
		"risks":[],"assumptions":[]
	}`)
	_, err = materializer.Materialize(MaterializationEvidence{
		Candidate: candidate, Sources: sources,
		WorkspacePathsObserved: true, WorkspacePathsComplete: true,
		WorkspacePaths: []string{"main.go", "main_test.go"},
	})
	if err == nil || !strings.Contains(err.Error(), "was not returned by workspace_tree") {
		t.Fatalf("unobserved modify path was accepted: %v", err)
	}

	added := bytes.Replace(candidate, []byte(`"action":"modify"`), []byte(`"action":"add"`), 1)
	if _, err := materializer.Materialize(MaterializationEvidence{
		Candidate: added, Sources: sources,
		WorkspacePathsObserved: true, WorkspacePathsComplete: true,
		WorkspacePaths: []string{"src/main.go"},
	}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("add action for existing path was accepted: %v", err)
	}
}

func TestWorkspaceMutationPolicySeparatesTestAndDeveloperAuthority(t *testing.T) {
	testPolicy, err := NewWorkspaceMutationPolicy(TestChangeSetContract, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := testPolicy.ValidateUpdate("main_test.go"); err != nil {
		t.Fatalf("existing test update rejected: %v", err)
	}
	if err := testPolicy.ValidateUpdate("main.go"); err == nil {
		t.Fatal("production update accepted for test-change-set")
	}
	if err := testPolicy.ValidateCreate("new_test.go"); err == nil {
		t.Fatal("test file creation accepted without explicit authority")
	}

	plan := []byte(`{"affectedPaths":[{"path":"main.go","action":"modify"},{"path":"new.go","action":"add"}]}`)
	developerPolicy, err := NewWorkspaceMutationPolicy(ChangeSetContract, []SourceArtifact{{Contract: ImplementationPlanContract, Content: plan}})
	if err != nil {
		t.Fatal(err)
	}
	if err := developerPolicy.ValidateUpdate("main.go"); err != nil {
		t.Fatalf("planned update rejected: %v", err)
	}
	if err := developerPolicy.ValidateCreate("new.go"); err != nil {
		t.Fatalf("planned create rejected: %v", err)
	}
	if err := developerPolicy.ValidateCreate("guessed.go"); err == nil {
		t.Fatal("unplanned create accepted")
	}
}

func TestWorkspaceMutationPolicySelectsOnlyAuthorizedInspectedPaths(t *testing.T) {
	testPolicy, err := NewWorkspaceMutationPolicy(TestChangeSetContract, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := testPolicy.AuthorizedUpdatePaths([]string{"src/main.go", "src/main_test.go", "src/main_test.go"})
	if !slices.Equal(paths, []string{"src/main_test.go"}) {
		t.Fatalf("authorized test paths = %v", paths)
	}

	plan := []byte(`{"affectedPaths":[{"path":"src/main.go","action":"modify"},{"path":"src/new.go","action":"add"}]}`)
	developerPolicy, err := NewWorkspaceMutationPolicy(ChangeSetContract, []SourceArtifact{{Contract: ImplementationPlanContract, Content: plan}})
	if err != nil {
		t.Fatal(err)
	}
	paths = developerPolicy.AuthorizedUpdatePaths([]string{"src/main_test.go", "src/new.go", "src/main.go"})
	if !slices.Equal(paths, []string{"src/main.go", "src/new.go"}) {
		t.Fatalf("authorized developer paths = %v", paths)
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
