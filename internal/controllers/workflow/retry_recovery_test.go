package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/utilitycontract"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCandidateFeedbackAndConsumedTestsSurviveAgentRetry(t *testing.T) {
	ctx := context.Background()
	r, workflow, failed, _ := recoveryAuditFixture(t)
	recovery, err := r.captureWorkflowRecovery(ctx, workflow, failed, "test-author")
	if err != nil {
		t.Fatal(err)
	}
	workflow.Status.WorkflowAttempt++
	workflow.Status.Refinement = &v1alpha1.WorkflowRefinementStatus{Iteration: workflow.Status.WorkflowAttempt, RestartStepName: "test-author", TriggerStepName: failed.Spec.StepName, TriggerAttemptRef: failed.Name, Recovery: recovery}
	step := workflow.Spec.Steps[0]
	step.Outputs = []v1alpha1.ContractReference{{Name: "test-change-set", Version: "v1"}}
	latest := &v1alpha1.FailedAgentAttempt{PreviousAttemptRef: "test-author-w001-r001", Code: "RepeatedToolCall", Message: "workspace_read repeated"}
	if err := r.createAttemptWithFeedback(ctx, workflow, step, 2, latest); err != nil {
		t.Fatal(err)
	}
	var run v1alpha1.AgentRun
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: workflow.Namespace, Name: "test-author-w001-r002"}, &run); err != nil {
		t.Fatal(err)
	}
	feedback := run.Spec.PriorAttemptRef
	if feedback == nil || feedback.PreviousAttemptRef != failed.Name || feedback.Code != utilitycontract.TestRunCodeError || !strings.Contains(feedback.Message, recovery.Feedback.Message) || !strings.Contains(feedback.Message, latest.Code) {
		t.Fatalf("lost recovery context: %#v", feedback)
	}
	var expected *v1alpha1.ArtifactReference
	for _, link := range recovery.InputLinks {
		if link.Input.Name == "test-change-set" {
			pin := link.Input
			expected = &pin
		}
	}
	if expected == nil {
		t.Fatal("fixture lacks consumed tests")
	}
	var found bool
	for _, pin := range run.Spec.Inputs {
		if pin.Name == "test-change-set" {
			found = true
			if pin.Digest != expected.Digest || *pin.ArtifactRef != *expected.ArtifactRef {
				t.Fatalf("selected unconsumed test artifact: %#v", pin)
			}
		}
	}
	if !found {
		t.Fatal("repair input omitted on second agent attempt")
	}
	if recovery.Feedback.Message != failed.Status.FailureMessage {
		t.Fatal("mutated frozen diagnostic")
	}
}

func TestCombinedRetryFeedbackRemainsBounded(t *testing.T) {
	original := &v1alpha1.FailedAgentAttempt{PreviousAttemptRef: "test-candidate", Code: utilitycontract.TestRunCodeError, Message: strings.Repeat("\u00e9", 512)}
	latest := &v1alpha1.FailedAgentAttempt{PreviousAttemptRef: "test-author", Code: "RepeatedToolCall", Message: strings.Repeat("x", 1024)}
	combined := mergeRetryFeedback(original, latest)
	if err := (agentcontract.RetryFeedback{PreviousAttemptRef: combined.PreviousAttemptRef, Code: combined.Code, Message: combined.Message}).Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(combined.Message, "RepeatedToolCall") || len(original.Message) != 1024 {
		t.Fatal("lost latest error or changed original")
	}
}
