package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/audit"
)

const terminationMessagePath = "/dev/termination-log"

type agentFailureDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func main() {
	inputPath := flag.String("input", "/workspace/control/input.json", "agent input contract")
	resultPath := flag.String("result", "", "agent result contract")
	auditPath := flag.String("audit-events", "", "agent wrapper audit event JSONL path")
	timeout := flag.Duration("timeout", 30*time.Minute, "maximum agent execution time")
	grace := flag.Duration("termination-grace", 10*time.Second, "grace before killing an interrupted agent")
	flag.Parse()

	// Parse input
	input, err := agentcontract.ReadInput(*inputPath)
	if err != nil {
		fail(resolveResultPath(*resultPath, agentcontract.Input{}), "InvalidInputContract", fmt.Errorf("invalid input: %w", err))
	}
	if *resultPath == "" {
		*resultPath = resolveResultPath("", input)
	}

	// Build auditor to track agent actions
	if *auditPath == "" {
		*auditPath = resolveAuditEventsPath(input, *resultPath)
	}
	auditor := audit.NewFileRecorder(*auditPath)
	events := newWrapperEvents(auditor, input)
	events.started(context.Background(), *inputPath, *resultPath, *auditPath)

	// Agent executable command config
	command, err := configuredCommand()
	if err != nil {
		events.invalidExecutable(context.Background(), err)
		fail(*resultPath, "InvalidAgentExecutable", err)
	}

	// Setup context to cancel with os.Interrupt or sigterm
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalContext, *timeout)
	defer cancel()

	// Execute agent runtime
	started := time.Now().UTC()
	events.runtimeStarted(ctx, command)
	if err := run(ctx, command, *inputPath, *resultPath, *grace); err != nil {
		events.runtimeFailed(context.Background(), command[0], err, time.Since(started))
		fail(*resultPath, "RuntimeFailed", err)
	}
	events.runtimeSucceeded(ctx, command[0], time.Since(started))

	// Read result file and validate artifacts. If invalid, reject and fail for a new attempt.
	result, err := agentcontract.ReadResultForInput(*resultPath, input)
	if err != nil {
		events.resultRejected(ctx, *resultPath, err)
		fail(*resultPath, "InvalidResultContract", fmt.Errorf("agent returned an invalid result: %w", err))
	}

	// Result was accepted, but outcome may still not be success
	events.resultAccepted(ctx, *resultPath, result)
	if result.Outcome != "Succeeded" {
		events.completedWithAgentFailure(ctx, result)
		if err := writeAgentFailureTerminationMessage(terminationMessagePath, result); err != nil {
			log.Printf("write agent failure termination message: %v", err)
		}
		log.Printf("agent completed with outcome %s: %s", result.Outcome, result.Message)
		os.Exit(1)
	}
	events.completed(ctx)
}

func writeAgentFailureTerminationMessage(path string, result agentcontract.Result) error {
	detail := agentFailureDetail{Code: "AgentReportedFailure", Message: result.Message}
	if result.Error != nil {
		detail.Code = result.Error.Code
		detail.Message = result.Error.Message
	}
	detail.Message = agentcontract.SanitizeRetryFeedbackMessage(detail.Message)
	if detail.Message == "" {
		detail.Message = "agent reported a failed outcome"
	}
	feedback := agentcontract.RetryFeedback{PreviousAttemptRef: "current-attempt", Code: detail.Code, Message: detail.Message}
	if err := feedback.Validate(); err != nil {
		detail.Code = "AgentReportedFailure"
	}
	data, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode termination message: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write termination message: %w", err)
	}
	return nil
}

func configuredCommand() ([]string, error) {
	value := os.Getenv("SOVEREIGN_AGENT_EXECUTABLE")
	if value == "" {
		return nil, fmt.Errorf("SOVEREIGN_AGENT_EXECUTABLE must contain a JSON command array")
	}
	var command []string
	if err := json.Unmarshal([]byte(value), &command); err != nil || len(command) == 0 || command[0] == "" {
		return nil, fmt.Errorf("SOVEREIGN_AGENT_EXECUTABLE must be a non-empty JSON command array")
	}
	return command, nil
}

func run(ctx context.Context, command []string, inputPath, resultPath string, grace time.Duration) error {
	// Build and execute run command
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = append(
		os.Environ(),
		"SOVEREIGN_INPUT_PATH="+inputPath,
		"SOVEREIGN_RESULT_PATH="+resultPath)
	cmd.Stdout = os.Stdout // pipe child to out+err
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	// wait until error or return
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("agent exited: %w", err)
		}
		return nil
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		select {
		case <-time.After(grace):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-done
		case <-done:
		}
		return fmt.Errorf("agent interrupted: %w", ctx.Err())
	}
}

func fail(resultPath, code string, err error) {
	log.Print(err)
	result := agentcontract.Result{
		SchemaVersion: agentcontract.Version,
		Outcome:       "Failed",
		Message:       err.Error(),
		Error:         &agentcontract.ResultError{Code: code, Message: err.Error()},
	}
	if writeErr := agentcontract.WriteResult(resultPath, result); writeErr != nil && !errors.Is(writeErr, os.ErrPermission) {
		log.Printf("write failure result: %v", writeErr)
	}
	os.Exit(1)
}

func resolveResultPath(flagValue string, input agentcontract.Input) string {
	if flagValue != "" {
		return flagValue
	}
	if input.ResultPath != "" {
		return input.ResultPath
	}
	return "/workspace/control/result.json"
}

func resolveAuditEventsPath(input agentcontract.Input, resultPath string) string {
	if input.AuditEventsPath != "" {
		return input.AuditEventsPath
	}
	return filepath.Join(filepath.Dir(resultPath), "events.jsonl")
}

type wrapperEvents struct {
	recorder audit.Recorder
	input    agentcontract.Input
}

func newWrapperEvents(recorder audit.Recorder, input agentcontract.Input) wrapperEvents {
	return wrapperEvents{recorder: recorder, input: input}
}

func (e wrapperEvents) started(ctx context.Context, inputPath, resultPath, auditPath string) {
	e.append(ctx, "AgentWrapperStarted", "start", "agent-wrapper", "started", "", map[string]string{
		"input":  inputPath,
		"result": resultPath,
		"events": auditPath,
	}, nil)
}

func (e wrapperEvents) invalidExecutable(ctx context.Context, err error) {
	e.append(ctx, "AgentWrapperCompleted", "complete", "agent-wrapper", "failed", "InvalidAgentExecutable", nil, map[string]string{"error": err.Error()})
}

func (e wrapperEvents) runtimeStarted(ctx context.Context, command []string) {
	e.append(ctx, "AgentRuntimeStarted", "start", command[0], "started", "", nil, map[string]any{"command": command})
}

func (e wrapperEvents) runtimeFailed(ctx context.Context, executable string, err error, duration time.Duration) {
	e.append(ctx, "AgentRuntimeExited", "complete", executable, "failed", "RuntimeFailed", nil, map[string]any{
		"error":          err.Error(),
		"durationMillis": duration.Milliseconds(),
	})
	e.append(ctx, "AgentWrapperCompleted", "complete", "agent-wrapper", "failed", "RuntimeFailed", nil, map[string]string{"error": err.Error()})
}

func (e wrapperEvents) runtimeSucceeded(ctx context.Context, executable string, duration time.Duration) {
	e.append(ctx, "AgentRuntimeExited", "complete", executable, "succeeded", "", nil, map[string]any{"durationMillis": duration.Milliseconds()})
}

func (e wrapperEvents) resultRejected(ctx context.Context, resultPath string, err error) {
	e.append(ctx, "AgentResultRejected", "validate", resultPath, "rejected", "InvalidResultContract", nil, map[string]string{"error": err.Error()})
	e.append(ctx, "AgentWrapperCompleted", "complete", "agent-wrapper", "failed", "InvalidResultContract", nil, map[string]string{"error": err.Error()})
}

func (e wrapperEvents) resultAccepted(ctx context.Context, resultPath string, result agentcontract.Result) {
	e.append(ctx, "AgentResultAccepted", "validate", resultPath, stringResultOutcome(result.Outcome), "", map[string]string{"result": resultPath}, map[string]any{"artifactCount": len(result.Artifacts)})
}

func (e wrapperEvents) completedWithAgentFailure(ctx context.Context, result agentcontract.Result) {
	reason := result.Outcome
	if result.Error != nil {
		reason = result.Error.Code
	}
	e.append(ctx, "AgentWrapperCompleted", "complete", "agent-wrapper", "failed", reason, nil, map[string]string{"message": result.Message})
}

func (e wrapperEvents) completed(ctx context.Context) {
	e.append(ctx, "AgentWrapperCompleted", "complete", "agent-wrapper", "succeeded", "", nil, nil)
}

func (e wrapperEvents) append(ctx context.Context, eventType, action, target, outcome, reason string, references map[string]string, data any) {
	if err := audit.AppendEvent(ctx, e.recorder, audit.EventOptions{
		Source:     "agent-wrapper",
		Type:       eventType,
		Actor:      audit.Actor{Kind: "RuntimeBoundary", ID: "agent-wrapper"},
		Subject:    audit.Subject{Workflow: e.input.WorkflowID, Step: e.input.StepName, Attempt: e.input.Attempt},
		Action:     action,
		Target:     target,
		Outcome:    outcome,
		Reason:     reason,
		References: references,
		Data:       data,
	}); err != nil {
		log.Printf("append wrapper audit event: %v", err)
	}
}

func stringResultOutcome(outcome string) string {
	if outcome == "" {
		return "unknown"
	}
	return strings.ToLower(outcome)
}
