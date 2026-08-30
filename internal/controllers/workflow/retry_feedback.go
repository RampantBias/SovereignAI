package workflow

import (
	"context"
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// workflowRetryFeedback reads the immutable triggering attempt, rather than
// copying failure state onto the workflow or modifying earlier agent runs.
func (r *WorkflowReconciler) workflowRetryFeedback(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, step v1alpha1.StepConfig) (*v1alpha1.FailedAgentAttempt, error) {
	refinement := workflow.Status.Refinement
	if step.Kind != v1alpha1.ExecutionKindAgent || workflow.Status.WorkflowAttempt < 1 ||
		refinement == nil || refinement.Iteration != workflow.Status.WorkflowAttempt ||
		refinement.RestartStepName != step.Name || refinement.TriggerAttemptRef == "" {
		return nil, nil
	}
	var failed v1alpha1.StepAttempt
	key := types.NamespacedName{Namespace: workflow.Namespace, Name: refinement.TriggerAttemptRef}
	if err := r.stepAttemptReader().Get(ctx, key, &failed); err != nil {
		return nil, fmt.Errorf("read workflow retry trigger: %w", err)
	}
	if failed.Spec.WorkflowRef.Name != workflow.Name || failed.Spec.WorkflowRef.UID != workflow.UID ||
		!metav1.IsControlledBy(&failed, workflow) || failed.Spec.StepName != refinement.TriggerStepName ||
		failed.Spec.WorkflowAttempt != workflow.Status.WorkflowAttempt-1 || failed.Status.Phase != v1alpha1.PhaseFailed {
		return nil, fmt.Errorf("workflow retry trigger %s does not match the previous failed workflow attempt", failed.Name)
	}
	return retryFeedbackForAttempt(&failed), nil
}
