package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SovereignAI/internal/utilitycontract"
)

func TestReadResultAcceptsUtilityContract(t *testing.T) {
	staging := t.TempDir()
	artifactPath := filepath.Join(staging, "commit.json")
	if err := os.WriteFile(artifactPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(staging, "result.json")
	if err := utilitycontract.WriteResult(resultPath, utilitycontract.Result{
		SchemaVersion: utilitycontract.Version,
		Outcome:       "Succeeded",
		Artifacts: []utilitycontract.ArtifactOutput{{
			Contract:  "commit/v1",
			Path:      artifactPath,
			MediaType: "application/json",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := readResult(resultPath, staging)
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion == utilitycontract.Version {
		t.Fatal("collector should convert utility results into the shared artifact collection shape")
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Contract != "commit/v1" {
		t.Fatalf("unexpected artifacts: %#v", result.Artifacts)
	}
}
