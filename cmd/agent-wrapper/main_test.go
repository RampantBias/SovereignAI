package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
)

func TestWriteAgentFailureTerminationMessagePreservesBoundedDiagnostic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	result := agentcontract.Result{
		Outcome: "Failed",
		Error: &agentcontract.ResultError{
			Code:    "InvalidChangedFile",
			Message: "changed file\tfailed\nvalidation\x00",
		},
	}
	if err := writeAgentFailureTerminationMessage(path, result); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var detail agentFailureDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Code != "InvalidChangedFile" || detail.Message != "changed file failed validation" {
		t.Fatalf("unexpected termination detail: %#v", detail)
	}
}
