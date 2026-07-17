package main

import (
	"context"
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
	events.operationSucceeded(ctx, time.Since(started), result)
	if err := utilitycontract.WriteResult(*resultPath, result); err != nil {
		events.resultRejected(context.Background(), err)
		fail(*resultPath, "UtilityResultWriteFailed", err)
	}
	events.completed(ctx)
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
	e.append(ctx, "UtilityOperationCompleted", "complete", e.input.Operation, "failed", "UtilityOperationFailed", nil, map[string]any{
		"error":          err.Error(),
		"durationMillis": duration.Milliseconds(),
	})
	e.append(ctx, "UtilityRunnerCompleted", "complete", "utility-runner", "failed", "UtilityOperationFailed", nil, map[string]string{"error": err.Error()})
}

func (e runnerEvents) operationSucceeded(ctx context.Context, duration time.Duration, result utilitycontract.Result) {
	e.append(ctx, "UtilityOperationCompleted", "complete", e.input.Operation, "succeeded", "", nil, map[string]any{
		"durationMillis": duration.Milliseconds(),
		"artifactCount":  len(result.Artifacts),
		"metadata":       result.Metadata,
	})
}

func (e runnerEvents) resultRejected(ctx context.Context, err error) {
	e.append(ctx, "UtilityResultRejected", "validate", e.input.ResultPath, "rejected", "UtilityResultWriteFailed", nil, map[string]string{"error": err.Error()})
}

func (e runnerEvents) completed(ctx context.Context) {
	e.append(ctx, "UtilityRunnerCompleted", "complete", "utility-runner", "succeeded", "", nil, nil)
}

func (e runnerEvents) append(ctx context.Context, eventType, action, target, outcome, reason string, references map[string]string, data any) {
	if err := audit.AppendEvent(ctx, e.recorder, audit.EventOptions{
		Source:     "utility-runner",
		Type:       eventType,
		Actor:      audit.Actor{Kind: "RuntimeBoundary", ID: "utility-runner"},
		Subject:    audit.Subject{Workflow: e.input.WorkflowID, Step: e.input.StepName, Attempt: e.input.Attempt},
		Action:     action,
		Target:     target,
		Outcome:    strings.ToLower(outcome),
		Reason:     reason,
		References: references,
		Data:       data,
	}); err != nil {
		log.Printf("append utility audit event: %v", err)
	}
}
