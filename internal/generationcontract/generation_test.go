package generationcontract

import (
	"encoding/json"
	"testing"

	"github.com/SovereignAI/internal/artifactcontract"
)

func TestChangeSetBindingFinalizesRuntimeFields(t *testing.T) {
	catalog, err := NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	patch := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"
	candidate, _ := json.Marshal(map[string]string{"summary": "change", "patch": patch})
	revision := []byte(`{"repositoryURL":"https://git.example.test/calculator.git","requestedRevision":"main","resolvedCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","utilityOperation":{"namespace":"workflow","name":"initialize-001","uid":"operation-uid"}}`)
	sources := []SourceArtifact{
		{Contract: artifactcontract.ChangeRequestContract, Digest: artifactcontract.DigestBytes([]byte("change"))},
		{Contract: artifactcontract.ImplementationPlanContract, Digest: artifactcontract.DigestBytes([]byte("plan"))},
		{Contract: artifactcontract.RepositoryRevisionContract, Digest: artifactcontract.DigestBytes(revision), Content: revision},
	}

	for _, contract := range []string{artifactcontract.TestChangeSetContract, artifactcontract.ChangeSetContract} {
		t.Run(contract, func(t *testing.T) {
			name, version := splitContract(contract)
			binding, err := catalog.Resolve(name, version)
			if err != nil {
				t.Fatal(err)
			}
			content, err := binding.Finalize(candidate, sources)
			if err != nil {
				t.Fatal(err)
			}
			if err := artifactcontract.ValidateContract(contract, content); err != nil {
				t.Fatalf("final artifact is invalid: %v", err)
			}
			var change artifactcontract.ChangeSet
			if err := json.Unmarshal(content, &change); err != nil {
				t.Fatal(err)
			}
			if change.Format != "unified-diff" || change.PatchDigest != artifactcontract.DigestBytes([]byte(patch)) ||
				len(change.Files) != 1 || change.Files[0] != "main.go" || change.LineCounts.Added != 1 || change.LineCounts.Deleted != 1 {
				t.Fatalf("derived patch fields are incorrect: %#v", change)
			}
		})
	}
}

func splitContract(contract string) (string, string) {
	for index, char := range contract {
		if char == '/' {
			return contract[:index], contract[index+1:]
		}
	}
	return contract, ""
}
