package orchestration

import (
	"github.com/SovereignAI/internal/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// vllmLabelPredicate filters for pods that are part of the vLLM server pool
func vllmLabelPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		labels := obj.GetLabels()
		return labels["app.kubernetes.io/name"] == "vllm-server"
	})
}

func vramPressurePredicate() predicate.Predicate {
	return predicate.Funcs{
		// Ignore creation events (a brand new GPU won't have pressure yet)
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		// Ignore deletion events (if the node dies, K8s GC handles the pods)
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
		// This is the critical one: only react to Updates
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldGPU, okOld := e.ObjectOld.(*v1alpha1.GPUNode)
			newGPU, okNew := e.ObjectNew.(*v1alpha1.GPUNode)
			if !okOld || !okNew {
				return false
			}

			oldPressure := oldGPU.Status.HasVRAMPressure
			newPressure := newGPU.Status.HasVRAMPressure

			// ONLY trigger if pressure changed
			// If false->true, we need to engage eviction policies
			// If true->false, we may need to unpause a Workflow
			if oldPressure != newPressure {
				return true
			}

			// Drop all other updates (like the DaemonSet just updating UsedVRAM by 10MB)
			return false
		},
	}
}
