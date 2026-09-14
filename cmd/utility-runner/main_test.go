package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SovereignAI/internal/audit"
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

func TestRunnerRecordsCandidateDecisionAndLinkedConsequences(t *testing.T) {
	recorder := audit.NewMemoryRecorder()
	events := newRunnerEvents(recorder, utilitycontract.Input{
		WorkflowID: "workflow", StepName: "candidate", Attempt: 1, Operation: "candidate.prepare",
		Authority:      utilitycontract.AuthorityReference{APIVersion: "aim.sovereign.io/v1alpha1", Kind: "UtilityOperation", Namespace: "workflow", Name: "candidate-001", UID: "utility-uid"},
		WorkspaceWrite: utilitycontract.WorkspaceWriteAuthority{LeaseName: "writer", HolderIdentity: "UtilityOperation/workflow/candidate-001/utility-uid", WriterEpoch: 4},
		Lineage:        &utilitycontract.LineageReferences{AdmissionDecisionEvent: "admission-event", AuthorityEvent: "authority-event", InputsEvent: "inputs-event", ExecutionDecisionEvent: "execution-event"},
	})
	events.operationCompleted(context.Background(), 25*time.Millisecond, utilitycontract.Result{
		Outcome:  "Succeeded",
		Metadata: map[string]string{"branch": "sovereign/workflow", "baseCommit": "base", "tree": "tree-digest"},
	})
	var evaluated, completed audit.Event
	for _, event := range recorder.AllEvents() {
		switch event.Type {
		case "CandidatePreparationEvaluated":
			evaluated = event
		case "UtilityOperationCompleted":
			completed = event
		}
	}
	if evaluated.ID == "" || completed.ID == "" {
		t.Fatalf("missing runtime lineage events: evaluated=%q completed=%q", evaluated.ID, completed.ID)
	}
	var decision audit.DecisionEvaluated
	if err := json.Unmarshal(evaluated.Data, &decision); err != nil {
		t.Fatal(err)
	}
	if decision.AuthorityEvent != "authority-event" || decision.InputEvent != "inputs-event" || decision.Decision.Outcome != "allowed" || len(decision.Invariants) < 4 {
		t.Fatalf("candidate evaluation did not preserve authority and checks: %#v", decision)
	}
	var consequence audit.ConsequenceRecorded
	if err := json.Unmarshal(completed.Data, &consequence); err != nil {
		t.Fatal(err)
	}
	if consequence.DecisionEvent != evaluated.ID || len(consequence.Consequences) < 3 {
		t.Fatalf("utility consequences were not linked to candidate evaluation: %#v", consequence)
	}
}
