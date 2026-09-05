package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
)

func TestBootstrapStoresExactValidatedChangeRequest(t *testing.T) {
	content := validBootstrapChangeRequest(t)
	root := t.TempDir()
	source := filepath.Join(root, "change-request.json")
	store := filepath.Join(root, "artifacts")
	if err := os.WriteFile(source, content, 0o440); err != nil {
		t.Fatal(err)
	}
	digest := artifactcontract.DigestBytes(content)
	if err := bootstrap(source, store, artifactcontract.ChangeRequestContract, digest); err != nil {
		t.Fatal(err)
	}
	stored, err := artifacts.Verify(store, digest)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.Bytes) != string(content) {
		t.Fatal("bootstrap did not preserve exact bytes")
	}
}

func TestBootstrapRejectsWrongDigestAndContract(t *testing.T) {
	content := validBootstrapChangeRequest(t)
	source := filepath.Join(t.TempDir(), "change-request.json")
	if err := os.WriteFile(source, content, 0o440); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap(source, t.TempDir(), artifactcontract.ChangeRequestContract, "sha256:"+strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest rejection, got %v", err)
	}
	if err := bootstrap(source, t.TempDir(), artifactcontract.RepositoryRevisionContract, artifactcontract.DigestBytes(content)); err == nil || !strings.Contains(err.Error(), "validate bootstrap") {
		t.Fatalf("expected contract rejection, got %v", err)
	}
}

func validBootstrapChangeRequest(t *testing.T) []byte {
	t.Helper()
	content, err := json.Marshal(artifactcontract.ChangeRequest{
		Summary: "Add divide support", Description: "Implement calculator division.",
		AcceptanceCriteria:          []artifactcontract.AcceptanceCriterionV1{{ID: "AC-001", Text: "84 / 2 returns 42", Digest: artifactcontract.CriterionDigest("AC-001", "84 / 2 returns 42")}},
		AcceptanceCriteriaSetDigest: artifactcontract.CriteriaSetDigest([]artifactcontract.CriterionIdentityV1{{ID: "AC-001", Digest: artifactcontract.CriterionDigest("AC-001", "84 / 2 returns 42")}}),
		RepositoryURL:               "https://git.example.test/calculator.git",
		SourceCommit:                strings.Repeat("a", 40),
	})
	if err != nil {
		t.Fatal(err)
	}
	return content
}
