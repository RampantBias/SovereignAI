package workflow

import (
	"context"
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifacts"
	"k8s.io/apimachinery/pkg/types"
)

// Seed repair from the exact test artifact consumed by the failed candidate.
// Downstream selection still requires this iteration's replacement artifact.
func (r *WorkflowReconciler) workflowRetryTestInput(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, step v1alpha1.StepConfig) (*v1alpha1.ArtifactReference, error) {
	refinement := workflow.Status.Refinement
	if refinement == nil || workflow.Status.WorkflowAttempt < 1 || refinement.Iteration != workflow.Status.WorkflowAttempt || refinement.RestartStepName != step.Name {
		return nil, nil
	}
	producesTests := false
	for _, output := range step.Outputs {
		producesTests = producesTests || output.Name == "test-change-set"
	}
	if !producesTests {
		return nil, nil
	}
	var links []v1alpha1.RecoveryInputLink
	if recovery := refinement.Recovery; recovery != nil {
		if recovery.FromWorkflowAttempt != workflow.Status.WorkflowAttempt-1 || recovery.TriggerAttempt.Name != refinement.TriggerAttemptRef {
			return nil, fmt.Errorf("test repair recovery does not match workflow iteration")
		}
		links = recovery.InputLinks
	} else {
		var trigger v1alpha1.StepAttempt
		if err := r.stepAttemptReader().Get(ctx, types.NamespacedName{Namespace: workflow.Namespace, Name: refinement.TriggerAttemptRef}, &trigger); err != nil {
			return nil, err
		}
		var err error
		links, _, err = r.recoveryInputPath(ctx, workflow, &trigger, step.Name, 2)
		if err != nil {
			return nil, err
		}
	}
	for _, link := range links {
		if link.Input.Name != "test-change-set" {
			continue
		}
		artifact, ready, reason, err := artifacts.ResolvePinnedInput(ctx, r.stepAttemptReader(), workflow.Namespace, recoveryUID(workflow), link.Input)
		if err != nil {
			return nil, err
		}
		if !ready || reason != "" || artifact.Spec.ProducerGrantRef != link.Producer {
			return nil, fmt.Errorf("prior test artifact is unavailable or no longer matches recovery evidence: %s", reason)
		}
		pin := link.Input
		return &pin, nil
	}
	return nil, nil
}
