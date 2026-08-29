package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SovereignAI/internal/utilitycontract"
)

func TestWriteOperationResultPreservesFailedEvidenceAndSignalsFailure(t *testing.T) {
	staging := t.TempDir()
	artifactPath := filepath.Join(staging, "test-report.json")
	if err := os.WriteFile(artifactPath, []byte(`{"outcome":"failed"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(staging, "control", "result.json")
	want := utilitycontract.Result{
		SchemaVersion: utilitycontract.Version,
		Outcome:       "Failed",
		Message:       "project test command produced failed",
		Error:         &utilitycontract.ResultError{Code: "TestRunFailed", Message: "tests failed"},
		Artifacts: []utilitycontract.ArtifactOutput{{
			Contract: "test-report/v1", Path: artifactPath, MediaType: "application/json",
		}},
	}
	succeeded, err := writeOperationResult(resultPath, want)
	if err != nil {
		t.Fatal(err)
	}
	if succeeded {
		t.Fatal("failed utility result was reported as process success")
	}
	got, err := utilitycontract.ReadResult(resultPath, staging)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "Failed" || got.Error == nil || got.Error.Code != "TestRunFailed" || len(got.Artifacts) != 1 {
		t.Fatalf("failed evidence was not preserved: %#v", got)
	}
}

func TestWriteOperationResultReportsSucceededOutcome(t *testing.T) {
	resultPath := filepath.Join(t.TempDir(), "result.json")
	succeeded, err := writeOperationResult(resultPath, utilitycontract.Result{
		SchemaVersion: utilitycontract.Version,
		Outcome:       "Succeeded",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !succeeded {
		t.Fatal("successful utility result was reported as process failure")
	}
}
