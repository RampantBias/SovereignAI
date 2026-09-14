package workflow

import (
	"context"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/utilitycontract"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWorkflowRetryFeedbackScope(t *testing.T) {
	for _, scenario := range []string{"current trigger", "second workflow retry", "environment failure", "missing diagnostic", "different step", "stale refinement", "wrong workflow", "wrong owner", "wrong generation", "not failed", "missing trigger"} {
		t.Run(scenario, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := v1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			workflow := changeRequestWorkflow()
			workflow.Status.WorkflowAttempt = 1
			workflow.Status.Refinement = &v1alpha1.WorkflowRefinementStatus{Iteration: 1, RestartStepName: "test-author", TriggerStepName: "test-candidate", TriggerAttemptRef: "test-candidate-w000-r001"}
			step := v1alpha1.StepConfig{Name: "test-author", Kind: v1alpha1.ExecutionKindAgent}
			failed := &v1alpha1.StepAttempt{
				ObjectMeta: metav1.ObjectMeta{Name: workflow.Status.Refinement.TriggerAttemptRef, Namespace: workflow.Namespace,
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(workflow, v1alpha1.GroupVersion.WithKind("SovereignWorkflow"))}},
				Spec:   v1alpha1.StepAttemptSpec{WorkflowRef: v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}, Kind: v1alpha1.ExecutionKindUtility, StepName: "test-candidate", RetryNumber: 1},
				Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhaseFailed, FailureReason: utilitycontract.TestRunCodeError, FailureMessage: "main_test.go:42: expected 2, got 3"},
			}
			wantFeedback, wantErr := false, false
			switch scenario {
			case "current trigger":
				wantFeedback = true
			case "second workflow retry":
				workflow.Status.WorkflowAttempt, workflow.Status.Refinement.Iteration = 2, 2
				failed.Spec.WorkflowAttempt = 1
				failed.Name = "test-candidate-w001-r001"
				workflow.Status.Refinement.TriggerAttemptRef = failed.Name
				wantFeedback = true
			case "environment failure":
				failed.Status.FailureReason = "TestRunFailed"
			case "missing diagnostic":
				failed.Status.FailureMessage = ""
			case "different step":
				step.Name = "developer"
			case "stale refinement":
				workflow.Status.Refinement.Iteration = 0
			case "wrong workflow":
				failed.Spec.WorkflowRef.UID = "other-workflow"
				wantErr = true
			case "wrong owner":
				failed.OwnerReferences[0].UID = "other-workflow"
				wantErr = true
			case "wrong generation":
				failed.Spec.WorkflowAttempt = 1
				wantErr = true
			case "not failed":
				failed.Status.Phase = v1alpha1.PhaseSucceeded
				wantErr = true
			case "missing trigger":
				workflow.Status.Refinement.TriggerAttemptRef = "missing"
				wantErr = true
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(failed).Build()
			r := &WorkflowReconciler{Client: kube, Reader: kube}
			feedback, err := r.workflowRetryFeedback(context.Background(), workflow, step)
			if (err != nil) != wantErr || (feedback != nil) != wantFeedback {
				t.Fatalf("feedback=%#v, err=%v; wantFeedback=%t, wantErr=%t", feedback, err, wantFeedback, wantErr)
			}
			if feedback != nil && (feedback.PreviousAttemptRef != failed.Name || feedback.Message != failed.Status.FailureMessage || feedback.Code != utilitycontract.TestRunCodeError) {
				t.Fatalf("feedback came from the wrong attempt: %#v", feedback)
			}
		})
	}
}
