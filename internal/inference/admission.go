package inference

import (
	"sort"

	"github.com/SovereignAI/internal/api/v1alpha1"
)

type Action string

const (
	ActionReuse  Action = "Reuse"
	ActionCreate Action = "Create"
	ActionEvict  Action = "Evict"
	ActionWait   Action = "Wait"
)

type Decision struct {
	Action      Action
	EndpointRef v1alpha1.NamespacedReference
	EvictLeases []v1alpha1.NamespacedReference
	Reason      string
}

type EndpointSnapshot struct {
	Endpoint v1alpha1.InferenceEndpoint
	Leases   []v1alpha1.InferenceLease
}

// Select policy evaluates admission capability for a new inference lease request
func Select(request v1alpha1.InferenceLease, snapshots []EndpointSnapshot, allowCreate bool) Decision {
	for _, snapshot := range snapshots {
		if !compatible(request, snapshot.Endpoint) {
			continue
		}
		if available(snapshot.Endpoint) >= request.Spec.EstimatedKVRAMMiB {
			return Decision{Action: ActionReuse, EndpointRef: reference(snapshot.Endpoint), Reason: "compatible warm endpoint has capacity"}
		}
		victims := evictionCandidates(request, snapshot.Leases)
		freed := int64(0)
		names := make([]v1alpha1.NamespacedReference, 0)
		for _, victim := range victims {
			freed += victim.Spec.EstimatedKVRAMMiB
			names = append(names, v1alpha1.NamespacedReference{Namespace: victim.Namespace, Name: victim.Name})
			if available(snapshot.Endpoint)+freed >= request.Spec.EstimatedKVRAMMiB {
				return Decision{Action: ActionEvict, EndpointRef: reference(snapshot.Endpoint), EvictLeases: names, Reason: "higher-priority lease can reclaim KV budget"}
			}
		}
	}
	if allowCreate {
		return Decision{Action: ActionCreate, Reason: "no compatible warm endpoint is available"}
	}
	return Decision{Action: ActionWait, Reason: "capacity is unavailable and endpoint creation is disabled"}
}

func compatible(lease v1alpha1.InferenceLease, endpoint v1alpha1.InferenceEndpoint) bool {
	if lease.Spec.Model != endpoint.Spec.Model || lease.Spec.ModelRevision != endpoint.Spec.ModelRevision {
		return false
	}
	if lease.Spec.Tenant != endpoint.Spec.Tenant || lease.Spec.Classification != endpoint.Spec.Classification {
		return false
	}
	switch lease.Spec.SharingScope {
	case v1alpha1.SharingDedicated:
		return false
	case v1alpha1.SharingWithinWorkflow:
		return endpoint.Labels["sovereign-ai.io/workflow-id"] == lease.Spec.WorkflowRef
	case v1alpha1.SharingWithinProject:
		return endpoint.Labels["sovereign-ai.io/project"] == lease.Spec.ProjectRef
	case v1alpha1.SharingWithinTenant, v1alpha1.SharingWithinClassification:
		return true
	default:
		return false
	}
}

func available(endpoint v1alpha1.InferenceEndpoint) int64 {
	capacity := endpoint.Spec.MaxKVRAMMiB - endpoint.Spec.SafetyHeadroomMiB - endpoint.Status.AllocatedKVRAMMiB
	if capacity < 0 {
		return 0
	}
	return capacity
}

func evictionCandidates(request v1alpha1.InferenceLease, leases []v1alpha1.InferenceLease) []v1alpha1.InferenceLease {
	candidates := make([]v1alpha1.InferenceLease, 0)
	for _, lease := range leases {
		if lease.Spec.Evictable && lease.Spec.Priority < request.Spec.Priority && lease.Status.Phase == v1alpha1.PhaseRunning {
			candidates = append(candidates, lease)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Spec.Priority == candidates[j].Spec.Priority {
			return candidates[i].CreationTimestamp.Before(&candidates[j].CreationTimestamp)
		}
		return candidates[i].Spec.Priority < candidates[j].Spec.Priority
	})
	return candidates
}

func reference(endpoint v1alpha1.InferenceEndpoint) v1alpha1.NamespacedReference {
	return v1alpha1.NamespacedReference{Namespace: endpoint.Namespace, Name: endpoint.Name}
}
