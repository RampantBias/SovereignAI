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
	workflowRef := v1alpha1.UIDReference{Name: "wf", UID: "wf-uid"}
	first, err := Collect(staging, store, workflowRef, producer, "abc", output)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Collect(staging, store, workflowRef, producer, "abc", output)
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
	workflowRef := v1alpha1.UIDReference{Name: "wf", UID: "wf-uid"}
	for _, contract := range []string{artifactcontract.ImplementationPlanContract, "unknown/v1", "invalid/v1/extra"} {
		t.Run(contract, func(t *testing.T) {
			_, err := Collect(staging, store, workflowRef, producer, "abc", []agentcontract.ArtifactOutput{{Contract: contract, Path: file}})
			if err == nil {
				t.Fatal("expected collection rejection")
			}
		})
	}

	if err := os.WriteFile(file, validImplementationPlan(t), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := Collect(staging, store, workflowRef, producer, "abc", []agentcontract.ArtifactOutput{{
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
	workflowRef := v1alpha1.UIDReference{Name: "wf", UID: "wf-uid"}
	producer := v1alpha1.TypedLocalReference{Kind: "AgentRun", Name: "architect-001"}
	_, err := Collect(staging, store, workflowRef, producer, "abc", []agentcontract.ArtifactOutput{{
		Contract: artifactcontract.ImplementationPlanContract, Path: file,
	}})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected stored digest mismatch, got %v", err)
	}
}

func TestCollectProjectsControllerReadableClaims(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	commit := strings.Repeat("c", 40)
	tree := strings.Repeat("d", 40)
	repository := "https://git.example.test/calculator.git"

	tests := []struct {
		name     string
		contract string
		value    any
		assert   func(*testing.T, *v1alpha1.ArtifactClaims)
	}{
		{
			name:     "candidate revision",
			contract: artifactcontract.CandidateRevisionContract,
			value: artifactcontract.CandidateRevision{
				RepositoryURL: repository, PreparedCandidateDigest: digestA,
				TestReportDigest: digestB, ChangeSetDigest: digestA,
				SourceCommit: strings.Repeat("a", 40), Branch: "sovereign/workflow-1",
				Commit: commit, Tree: tree, CommitMessageDigest: digestB,
			},
			assert: func(t *testing.T, claims *v1alpha1.ArtifactClaims) {
				t.Helper()
				if claims == nil || claims.CandidateRevision == nil ||
					claims.CandidateRevision.Commit != commit ||
					claims.CandidateRevision.Tree != tree ||
					claims.CandidateRevision.RepositoryURL != repository {
					t.Fatalf("unexpected candidate revision claims: %#v", claims)
				}
			},
		},
		{
			name:     "candidate remote proof",
			contract: artifactcontract.CandidateRemoteProofContract,
			value: artifactcontract.CandidateRemoteProof{
				CandidateRevisionDigest: digestA, RepositoryURL: repository,
				Ref: "refs/heads/sovereign/workflow-1", ObservedCommit: commit,
				VerifiedAt: "2026-08-29T12:00:00Z",
			},
			assert: func(t *testing.T, claims *v1alpha1.ArtifactClaims) {
				t.Helper()
				if claims == nil || claims.CandidateRemoteProof == nil ||
					claims.CandidateRemoteProof.CandidateRevisionDigest != digestA ||
					claims.CandidateRemoteProof.ObservedCommit != commit {
					t.Fatalf("unexpected candidate remote proof claims: %#v", claims)
				}
			},
		},
		{
			name:     "image digest",
			contract: artifactcontract.ImageDigestContract,
			value: artifactcontract.ImageDigest{
				CandidateRevisionDigest: digestA,
				ImageRepository:         "registry.example.test/sovereign/calculator",
				Digest:                  digestB,
				CandidateCommit:         commit,
				CandidateTree:           tree,
				BuilderImageDigest:      digestA,
				BuildCommandDigest:      digestB,
				DockerfileDigest:        digestA,
				ContextTree:             tree,
			},
			assert: func(t *testing.T, claims *v1alpha1.ArtifactClaims) {
				t.Helper()
				if claims == nil || claims.ImageDigest == nil ||
					claims.ImageDigest.ImageRepository != "registry.example.test/sovereign/calculator" ||
					claims.ImageDigest.OCIDigest != digestB ||
					claims.ImageDigest.CandidateCommit != commit {
					t.Fatalf("unexpected image digest claims: %#v", claims)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			staging := t.TempDir()
			content, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(staging, "artifact.json")
			if err := os.WriteFile(path, content, 0o640); err != nil {
				t.Fatal(err)
			}
			collected, err := Collect(
				staging,
				t.TempDir(),
				v1alpha1.UIDReference{Name: "wf", UID: "wf-uid"},
				v1alpha1.TypedLocalReference{Kind: "UtilityOperation", Name: "attempt"},
				commit,
				[]agentcontract.ArtifactOutput{{Contract: test.contract, Path: path}},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(collected) != 1 {
				t.Fatalf("collected %d artifacts, want 1", len(collected))
			}
			test.assert(t, collected[0].Spec.Claims)
		})
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
