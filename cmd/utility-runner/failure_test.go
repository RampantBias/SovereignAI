package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

func TestUtilityFailureTerminationMessage(t *testing.T) {
	for _, tt := range []struct {
		name          string
		result        utilitycontract.Result
		code, message string
	}{
		{"code diagnostic", utilitycontract.Result{Outcome: "Failed", Message: "generic failure", Error: &utilitycontract.ResultError{
			Code: utilitycontract.TestRunCodeError, Message: "main_test.go:42: expected 2, got 3",
		}}, utilitycontract.TestRunCodeError, "main_test.go:42: expected 2, got 3"},
		{"missing error", utilitycontract.Result{Outcome: "Failed"}, "UtilityReportedFailure", "utility reported a failed outcome"},
		{"invalid code", utilitycontract.Result{Error: &utilitycontract.ResultError{Code: "bad code", Message: "failed\nnow"}}, "UtilityReportedFailure", "failed now"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "termination-log")
			if err := writeUtilityFailureTerminationMessage(path, tt.result); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var detail utilitycontract.ResultError
			if err := json.Unmarshal(data, &detail); err != nil {
				t.Fatal(err)
			}
			if detail.Code != tt.code || detail.Message != tt.message {
				t.Fatalf("termination detail = %#v", detail)
			}
		})
	}
}

func TestUtilityFailureTerminationMessageFitsContainerLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	result := utilitycontract.Result{Error: &utilitycontract.ResultError{Code: utilitycontract.TestRunCodeError, Message: strings.Repeat("<>&界", 2000)}}
	if err := writeUtilityFailureTerminationMessage(path, result); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var detail utilitycontract.ResultError
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatal(err)
	}
	if len(data) > 4096 || len(detail.Message) > agentcontract.MaxRetryFeedbackMessageBytes {
		t.Fatalf("termination detail exceeds bounds: encoded=%d, message=%d", len(data), len(detail.Message))
	}
}
