package state

import (
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
)

func TestValidateTransition(t *testing.T) {
	tests := []struct {
		name    string
		from    v1alpha1.ResourcePhase
		to      v1alpha1.ResourcePhase
		wantErr bool
	}{
		{name: "initialize", to: v1alpha1.PhasePending},
		{name: "execute", from: v1alpha1.PhasePreparing, to: v1alpha1.PhaseRunning},
		{name: "retry interruption", from: v1alpha1.PhaseInterrupted, to: v1alpha1.PhaseRetrying},
		{name: "terminal cannot restart", from: v1alpha1.PhaseSucceeded, to: v1alpha1.PhaseRunning, wantErr: true},
		{name: "skip admission", from: v1alpha1.PhasePending, to: v1alpha1.PhaseRunning, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTransition(tt.from, tt.to)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateTransition() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestNextAttemptNumberDoesNotOverwriteHistory(t *testing.T) {
	attempts := []v1alpha1.StepAttempt{
		{Spec: v1alpha1.StepAttemptSpec{StepName: "developer", RetryNumber: 1, WorkflowAttempt: 1}},
		{Spec: v1alpha1.StepAttemptSpec{StepName: "architect", RetryNumber: 1, WorkflowAttempt: 1}},
		{Spec: v1alpha1.StepAttemptSpec{StepName: "developer", RetryNumber: 2, WorkflowAttempt: 1}},
	}
	if got := NextAttemptNumber(attempts, "developer"); got != 3 {
		t.Fatalf("NextAttemptNumber() = %d, want 3", got)
	}
}

func TestNextRetryNumberResetsForWorkflowAttempt(t *testing.T) {
	attempts := []v1alpha1.StepAttempt{
		{Spec: v1alpha1.StepAttemptSpec{StepName: "test-author", RetryNumber: 1, WorkflowAttempt: 0}},
		{Spec: v1alpha1.StepAttemptSpec{StepName: "test-author", RetryNumber: 2, WorkflowAttempt: 0}},
		{Spec: v1alpha1.StepAttemptSpec{StepName: "test-author", RetryNumber: 1, WorkflowAttempt: 1}},
		{Spec: v1alpha1.StepAttemptSpec{StepName: "developer", RetryNumber: 4, WorkflowAttempt: 1}},
	}

	if got := NextRetryNumber(attempts, "test-author", 1); got != 2 {
		t.Fatalf("NextRetryNumber(workflow attempt 1) = %d, want 2", got)
	}
	if got := NextRetryNumber(attempts, "test-author", 2); got != 1 {
		t.Fatalf("NextRetryNumber(workflow attempt 2) = %d, want reset to 1", got)
	}
}
