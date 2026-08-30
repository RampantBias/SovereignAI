package workflow

import (
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"
)

type failureAction string

const (
	failureActionRetryStep     failureAction = "RetryStep"
	failureActionRetryWorkflow failureAction = "RetryWorkflow"
	failureActionFailWorkflow  failureAction = "FailWorkflow"
)

type failureDecision struct {
	Action      failureAction
	RestartStep string
}

// hand-writing test-candidate for now, everything else would default fail
var stepFailurePolicies = map[string]failureDecision{
	"test-candidate": {
		Action:      failureActionRetryWorkflow,
		RestartStep: "test-author",
	},
}

func decideFailure(
	steps []v1alpha1.StepConfig,
	failedStep v1alpha1.StepConfig,
	phase v1alpha1.ResourcePhase,
) (failureDecision, error) {
	decision, configured := stepFailurePolicies[failedStep.Name]
	if !configured {
		return failureDecision{
			Action: failureActionRetryStep,
		}, nil
	}

	if decision.Action != failureActionRetryWorkflow {
		return decision, nil
	}

	// An interruption represents an execution-level problem. It is not
	// evidence that earlier workflow results need to be recomputed.
	if phase != v1alpha1.PhaseFailed {
		return failureDecision{
			Action: failureActionRetryStep,
		}, nil
	}

	failedIndex := stepIndex(steps, failedStep.Name)
	if failedIndex < 0 {
		return failureDecision{}, fmt.Errorf(
			"failed step %q is not part of the workflow",
			failedStep.Name,
		)
	}

	restartIndex := stepIndex(steps, decision.RestartStep)
	if restartIndex < 0 {
		return failureDecision{}, fmt.Errorf(
			"workflow retry target %q does not exist",
			decision.RestartStep,
		)
	}

	if restartIndex >= failedIndex {
		return failureDecision{}, fmt.Errorf(
			"workflow retry target %q must precede failed step %q",
			decision.RestartStep,
			failedStep.Name,
		)
	}

	return decision, nil
}

func stepIndex(steps []v1alpha1.StepConfig, name string) int {
	for index := range steps {
		if steps[index].Name == name {
			return index
		}
	}

	return -1
}
