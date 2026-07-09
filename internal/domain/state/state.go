package state

import (
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"
)

var transitions = map[v1alpha1.ResourcePhase]map[v1alpha1.ResourcePhase]struct{}{
	"": {
		v1alpha1.PhasePending: {},
	},
	v1alpha1.PhasePending: {
		v1alpha1.PhaseAdmitted:  {},
		v1alpha1.PhaseCancelled: {},
		v1alpha1.PhaseFailed:    {},
	},
	v1alpha1.PhaseAdmitted: {
		v1alpha1.PhasePreparing: {},
		v1alpha1.PhaseCancelled: {},
		v1alpha1.PhaseFailed:    {},
	},
	v1alpha1.PhasePreparing: {
		v1alpha1.PhaseRunning:   {},
		v1alpha1.PhaseRetrying:  {},
		v1alpha1.PhaseFailed:    {},
		v1alpha1.PhaseCancelled: {},
	},
	v1alpha1.PhaseRunning: {
		v1alpha1.PhaseAwaitingApproval: {},
		v1alpha1.PhaseIntervening:      {},
		v1alpha1.PhaseValidating:       {},
		v1alpha1.PhaseSucceeded:        {},
		v1alpha1.PhaseInterrupted:      {},
		v1alpha1.PhaseRetrying:         {},
		v1alpha1.PhaseFailed:           {},
		v1alpha1.PhaseCancelled:        {},
	},
	v1alpha1.PhaseAwaitingApproval: {
		v1alpha1.PhaseRunning:     {},
		v1alpha1.PhaseIntervening: {},
		v1alpha1.PhaseValidating:  {},
		v1alpha1.PhaseSucceeded:   {},
		v1alpha1.PhaseFailed:      {},
		v1alpha1.PhaseCancelled:   {},
	},
	v1alpha1.PhaseIntervening: {
		v1alpha1.PhaseRunning:   {},
		v1alpha1.PhaseSucceeded: {},
		v1alpha1.PhaseFailed:    {},
		v1alpha1.PhaseCancelled: {},
	},
	v1alpha1.PhaseValidating: {
		v1alpha1.PhaseSucceeded: {},
		v1alpha1.PhaseFailed:    {},
		v1alpha1.PhaseRetrying:  {},
		v1alpha1.PhaseCancelled: {},
	},
	v1alpha1.PhaseInterrupted: {
		v1alpha1.PhaseRetrying:  {},
		v1alpha1.PhaseFailed:    {},
		v1alpha1.PhaseCancelled: {},
	},
	v1alpha1.PhaseRetrying: {
		v1alpha1.PhasePreparing: {},
		v1alpha1.PhaseFailed:    {},
		v1alpha1.PhaseCancelled: {},
	},
}

func ValidateTransition(from, to v1alpha1.ResourcePhase) error {
	if from == to {
		return nil
	}
	allowed, ok := transitions[from]
	if !ok {
		return fmt.Errorf("phase %q is terminal or unknown", from)
	}
	if _, ok := allowed[to]; !ok {
		return fmt.Errorf("transition %q -> %q is not allowed", from, to)
	}
	return nil
}

func IsTerminal(phase v1alpha1.ResourcePhase) bool {
	switch phase {
	case v1alpha1.PhaseSucceeded, v1alpha1.PhaseFailed, v1alpha1.PhaseCancelled:
		return true
	default:
		return false
	}
}

func NextAttemptNumber(attempts []v1alpha1.StepAttempt, stepName string) int32 {
	var max int32
	for _, attempt := range attempts {
		if attempt.Spec.StepName == stepName && attempt.Spec.Attempt > max {
			max = attempt.Spec.Attempt
		}
	}
	return max + 1
}
