package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/utility"
	"github.com/SovereignAI/internal/utilitycontract"
)

func main() {
	inputPath := flag.String("input", "/workspace/control/input.json", "utility input contract")
	resultPath := flag.String("result", "", "utility result contract")
	auditPath := flag.String("audit-events", "", "utility runner audit event JSONL path")
	timeout := flag.Duration("timeout", 30*time.Minute, "maximum utility execution time")
	flag.Parse()

	input, err := utilitycontract.ReadInput(*inputPath)
	if err != nil {
		fail(resolveResultPath(*resultPath, utilitycontract.Input{}), "InvalidInputContract", fmt.Errorf("invalid input: %w", err))
	}
	if *resultPath == "" {
		*resultPath = resolveResultPath("", input)
	}
	if *auditPath == "" {
		*auditPath = resolveAuditEventsPath(input, *resultPath)
	}

	auditor := audit.NewFileRecorder(*auditPath)
	events := newRunnerEvents(auditor, input)
	events.started(context.Background(), *inputPath, *resultPath, *auditPath)

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalContext, *timeout)
	defer cancel()

	started := time.Now().UTC()
	events.operationStarted(ctx)
	result, err := utility.DefaultRegistry().Run(ctx, input)
	if err != nil {
		events.operationFailed(context.Background(), err, time.Since(started))
		fail(*resultPath, "UtilityOperationFailed", err)
	}
	events.operationCompleted(ctx, time.Since(started), result)
	succeeded, err := writeOperationResult(*resultPath, result)
	if err != nil {
		events.resultRejected(context.Background(), err)
		fail(*resultPath, "UtilityResultWriteFailed", err)
	}
	if !succeeded {
		events.completedWithResult(context.Background(), result)
		if err := writeUtilityFailureTerminationMessage(terminationMessagePath, result); err != nil {
			log.Print(err)
		}
		log.Print(result.Message)
		os.Exit(1)
	}
	events.completed(ctx)
}

func writeOperationResult(path string, result utilitycontract.Result) (bool, error) {
	if err := utilitycontract.WriteResult(path, result); err != nil {
		return false, err
	}
	return result.Outcome == "Succeeded", nil
}

func fail(resultPath, code string, err error) {
	log.Print(err)
	result := utilitycontract.Result{
		SchemaVersion: utilitycontract.Version,
		Outcome:       "Failed",
		Message:       err.Error(),
		Error:         &utilitycontract.ResultError{Code: code, Message: err.Error()},
	}
	if writeErr := utilitycontract.WriteResult(resultPath, result); writeErr != nil && !errors.Is(writeErr, os.ErrPermission) {
		log.Printf("write utility failure result: %v", writeErr)
	}
	if writeErr := writeUtilityFailureTerminationMessage(terminationMessagePath, result); writeErr != nil {
		log.Print(writeErr)
	}
	os.Exit(1)
}

func resolveResultPath(flagValue string, input utilitycontract.Input) string {
	if flagValue != "" {
		return flagValue
	}
	if input.ResultPath != "" {
		return input.ResultPath
	}
	return "/workspace/control/result.json"
}

func resolveAuditEventsPath(input utilitycontract.Input, resultPath string) string {
	if input.AuditEventsPath != "" {
		return input.AuditEventsPath
	}
	return filepath.Join(filepath.Dir(resultPath), "events.jsonl")
}

type runnerEvents struct {
	recorder audit.Recorder
	input    utilitycontract.Input
}

func newRunnerEvents(recorder audit.Recorder, input utilitycontract.Input) runnerEvents {
	return runnerEvents{recorder: recorder, input: input}
}

func (e runnerEvents) started(ctx context.Context, inputPath, resultPath, auditPath string) {
	e.append(ctx, "UtilityRunnerStarted", "start", "utility-runner", "started", "", map[string]string{
		"input":  inputPath,
		"result": resultPath,
		"events": auditPath,
	}, nil)
}

func (e runnerEvents) operationStarted(ctx context.Context) {
	e.append(ctx, "UtilityOperationStarted", "start", e.input.Operation, "started", "", nil, map[string]any{
		"operation":        e.input.Operation,
		"authority":        e.input.Authority,
		"policyDecisionId": e.input.PolicyDecisionID,
		"idempotencyKey":   e.input.IdempotencyKey,
		"parameters":       e.input.Parameters,
		"command":          e.input.Command,
		"credentialClass":  e.input.CredentialClass,
	})
}

func (e runnerEvents) operationFailed(ctx context.Context, err error, duration time.Duration) {
	decisionEvent := e.executionDecisionEvent()
	if e.input.Operation == utility.OperationCandidatePrepare {
		decisionEvent = e.candidatePreparationEvaluated(ctx, utilitycontract.Result{}, err)
	}
	e.append(ctx, "UtilityOperationCompleted", "complete", e.input.Operation, "failed", "UtilityOperationFailed", nil, audit.ConsequenceRecorded{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		DecisionEvent: decisionEvent,
		Consequences: []audit.ConsequenceEvidence{
			{SchemaVersion: audit.PayloadSchemaVersionV1, Kind: "runtime-outcome", Target: e.input.Operation, After: "failed"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, Kind: "runtime-duration", Target: "milliseconds", After: fmt.Sprint(duration.Milliseconds())},
		},
	})
	e.append(ctx, "UtilityRunnerCompleted", "complete", "utility-runner", "failed", "UtilityOperationFailed", nil, map[string]string{"error": err.Error()})
}

func (e runnerEvents) operationCompleted(ctx context.Context, duration time.Duration, result utilitycontract.Result) {
	reason := ""
	if result.Error != nil {
		reason = result.Error.Code
	}
	decisionEvent := e.executionDecisionEvent()
	if e.input.Operation == utility.OperationCandidatePrepare {
		decisionEvent = e.candidatePreparationEvaluated(ctx, result, nil)
	}
	consequences := utilityConsequences(result)
	consequences = append(consequences, audit.ConsequenceEvidence{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Kind:          "runtime-duration",
		Target:        "milliseconds",
		After:         fmt.Sprint(duration.Milliseconds()),
	})
	e.append(ctx, "UtilityOperationCompleted", "complete", e.input.Operation, strings.ToLower(result.Outcome), reason, nil, audit.ConsequenceRecorded{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		DecisionEvent: decisionEvent,
		Consequences:  consequences,
	})
}

func (e runnerEvents) candidatePreparationEvaluated(ctx context.Context, result utilitycontract.Result, runErr error) string {
	errorMessage := ""
	if runErr != nil {
		errorMessage = runErr.Error()
	}
	decisionInput, err := json.Marshal(struct {
		Inputs         []utilitycontract.ArtifactInput         `json:"inputs"`
		Parameters     map[string]string                       `json:"parameters"`
		WorkspaceWrite utilitycontract.WorkspaceWriteAuthority `json:"workspaceWrite"`
		Metadata       map[string]string                       `json:"metadata,omitempty"`
		Error          string                                  `json:"error,omitempty"`
	}{e.input.Inputs, e.input.Parameters, e.input.WorkspaceWrite, result.Metadata, errorMessage})
	if err != nil {
		log.Printf("marshal candidate preparation evaluation: %v", err)
		return e.executionDecisionEvent()
	}
	inputDigest := artifactcontract.DigestBytes(decisionInput)
	outcome := "passed"
	decisionOutcome := "allowed"
	invariants := []audit.InvariantResult{
		{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "candidate-input-contracts-valid", Outcome: "passed", Expected: "repository-revision/v1, test-change-set/v1, change-set/v1", Observed: "validated", Reason: "candidate.prepare returned only after all typed input contracts and digests were accepted"},
		{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "candidate-source-plan-lineage-consistent", Outcome: "passed", Expected: "one base commit, change request, and implementation plan", Observed: "consistent", Reason: "test and implementation changes were checked against the same accepted lineage"},
		{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "candidate-workspace-authority-current", Outcome: "passed", Expected: fmt.Sprintf("%s@%d", e.input.WorkspaceWrite.HolderIdentity, e.input.WorkspaceWrite.WriterEpoch), Observed: fmt.Sprintf("%s@%d", e.input.WorkspaceWrite.HolderIdentity, e.input.WorkspaceWrite.WriterEpoch), Reason: "candidate preparation ran under the fenced writer term admitted by the controller"},
		{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "candidate-tree-materialized", Outcome: "passed", Expected: "git tree digest", Observed: result.Metadata["tree"], Reason: "the prepared candidate records the exact resulting Git tree"},
	}
	if runErr != nil {
		outcome = "failed"
		decisionOutcome = "denied"
		invariants = []audit.InvariantResult{{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "candidate-preparation-completed", Outcome: "failed", Expected: "all candidate preparation invariants pass", Observed: errorMessage, Reason: "candidate preparation stopped at an enforced invariant"}}
	}
	payload := audit.DecisionEvaluated{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Primitive: audit.ResourceRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			APIVersion:    e.input.Authority.APIVersion,
			Kind:          e.input.Authority.Kind,
			Namespace:     e.input.Authority.Namespace,
			Name:          e.input.Authority.Name,
			UID:           e.input.Authority.UID,
		},
		Decision: audit.DecisionRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			ID:            audit.DeterministicID("candidate-preparation-evaluation", e.input.Authority.UID, inputDigest),
			Kind:          "controller",
			Revision:      "candidate.prepare/v1",
			InputDigest:   inputDigest,
			Outcome:       decisionOutcome,
		},
		Invariants: invariants,
	}
	if e.input.Lineage != nil {
		payload.AuthorityEvent = e.input.Lineage.AuthorityEvent
		payload.InputEvent = e.input.Lineage.InputsEvent
		payload.EvidenceEvents = []string{e.input.Lineage.AdmissionDecisionEvent, e.input.Lineage.ExecutionDecisionEvent}
	}
	reason := ""
	if runErr != nil {
		reason = "CandidatePreparationInvariantFailed"
	}
	event := e.append(ctx, "CandidatePreparationEvaluated", "evaluate", e.input.Operation, outcome, reason, nil, payload)
	return event.ID
}

func (e runnerEvents) executionDecisionEvent() string {
	if e.input.Lineage == nil {
		return ""
	}
	return e.input.Lineage.ExecutionDecisionEvent
}

func utilityConsequences(result utilitycontract.Result) []audit.ConsequenceEvidence {
	consequences := []audit.ConsequenceEvidence{{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Kind:          "runtime-outcome",
		After:         strings.ToLower(result.Outcome),
	}}
	for _, artifact := range result.Artifacts {
		evidence := audit.ConsequenceEvidence{SchemaVersion: audit.PayloadSchemaVersionV1, Kind: "artifact-output", Target: artifact.Contract}
		if data, err := os.ReadFile(artifact.Path); err == nil {
			evidence.Digest = artifactcontract.DigestBytes(data)
		}
		consequences = append(consequences, evidence)
	}
	for _, key := range []string{"branch", "commit", "tree", "remote", "ref"} {
		if value := result.Metadata[key]; value != "" {
			consequences = append(consequences, audit.ConsequenceEvidence{SchemaVersion: audit.PayloadSchemaVersionV1, Kind: "git-" + key, Target: key, After: value})
		}
	}
	if digest := result.Metadata["digest"]; digest != "" {
		consequences = append(consequences, audit.ConsequenceEvidence{SchemaVersion: audit.PayloadSchemaVersionV1, Kind: "image-digest", Digest: digest})
	}
	if previous := result.Metadata["previousRemoteCommit"]; previous != "" {
		consequences = append(consequences, audit.ConsequenceEvidence{SchemaVersion: audit.PayloadSchemaVersionV1, Kind: "git-remote-transition", Before: previous, After: result.Metadata["commit"]})
	}
	if number := result.Metadata["number"]; number != "" || result.Metadata["url"] != "" {
		consequences = append(consequences, audit.ConsequenceEvidence{SchemaVersion: audit.PayloadSchemaVersionV1, Kind: "merge-request", ExternalID: number, Target: result.Metadata["url"], Before: result.Metadata["source"], After: result.Metadata["target"]})
	}
	return consequences
}

func (e runnerEvents) resultRejected(ctx context.Context, err error) {
	e.append(ctx, "UtilityResultRejected", "validate", e.input.ResultPath, "rejected", "UtilityResultWriteFailed", nil, map[string]string{"error": err.Error()})
}

func (e runnerEvents) completed(ctx context.Context) {
	e.append(ctx, "UtilityRunnerCompleted", "complete", "utility-runner", "succeeded", "", nil, nil)
}

func (e runnerEvents) completedWithResult(ctx context.Context, result utilitycontract.Result) {
	reason := ""
	data := map[string]string{"message": result.Message}
	if result.Error != nil {
		reason = result.Error.Code
		data["error"] = result.Error.Message
	}
	e.append(ctx, "UtilityRunnerCompleted", "complete", "utility-runner", strings.ToLower(result.Outcome), reason, nil, data)
}

func (e runnerEvents) append(ctx context.Context, eventType, action, target, outcome, reason string, references map[string]string, data any) audit.Event {
	if references == nil {
		references = map[string]string{}
	}
	references["utilityOperation"] = e.input.Authority.Name
	decisionID := ""
	if payload, ok := data.(audit.DecisionEvaluated); ok {
		decisionID = payload.Decision.ID
	}
	event, err := audit.BuildAndAppendEvent(ctx, e.recorder, audit.EventOptions{
		Source:     "utility-runner",
		Type:       eventType,
		Actor:      audit.Actor{Kind: "RuntimeBoundary", ID: "utility-runner"},
		Subject:    audit.Subject{Workflow: e.input.WorkflowID, Step: e.input.StepName, Attempt: e.input.Attempt},
		Action:     action,
		Target:     target,
		Outcome:    strings.ToLower(outcome),
		Reason:     reason,
		DecisionID: decisionID,
		References: references,
		Data:       data,
	})
	if err != nil {
		log.Printf("append utility audit event: %v", err)
		return audit.Event{}
	}
	return event
}
