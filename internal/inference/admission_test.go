package inference

import (
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSelectReusesCompatibleEndpoint(t *testing.T) {
	endpoint := endpointFixture()
	request := leaseFixture("request", 2000, 10, false)
	decision := Select(request, []EndpointSnapshot{{Endpoint: endpoint}}, false)
	if decision.Action != ActionReuse || decision.EndpointRef.Name != endpoint.Name {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func TestSelectRejectsClassificationMismatch(t *testing.T) {
	endpoint := endpointFixture()
	request := leaseFixture("request", 2000, 10, false)
	request.Spec.Classification = "classified"
	decision := Select(request, []EndpointSnapshot{{Endpoint: endpoint}}, false)
	if decision.Action != ActionWait {
		t.Fatalf("incompatible endpoint was selected: %#v", decision)
	}
}

func TestSelectEvictsLowerPriorityLeases(t *testing.T) {
	endpoint := endpointFixture()
	endpoint.Status.AllocatedKVRAMMiB = 7000
	request := leaseFixture("important", 2500, 100, false)
	victim := leaseFixture("background", 3000, 1, true)
	victim.Status.Phase = v1alpha1.PhaseRunning
	decision := Select(request, []EndpointSnapshot{{Endpoint: endpoint, Leases: []v1alpha1.InferenceLease{victim}}}, false)
	if decision.Action != ActionEvict || len(decision.EvictLeases) != 1 || decision.EvictLeases[0].Name != victim.Name {
		t.Fatalf("unexpected eviction decision: %#v", decision)
	}
}

func endpointFixture() v1alpha1.InferenceEndpoint {
	return v1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "code", Namespace: "sovereign-inference", Labels: map[string]string{"sovereign-ai.io/project": "project"}},
		Spec:       v1alpha1.InferenceEndpointSpec{Model: "code", ModelRevision: "v1", Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject, MaxKVRAMMiB: 10000, SafetyHeadroomMiB: 1000},
	}
}

func leaseFixture(name string, kv int64, priority int32, evictable bool) v1alpha1.InferenceLease {
	return v1alpha1.InferenceLease{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.InferenceLeaseSpec{WorkflowRef: v1alpha1.UIDReference{Name: "wf"}, ProjectRef: "project", Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject, Model: "code", ModelRevision: "v1", EstimatedKVRAMMiB: kv, Priority: priority, Evictable: evictable},
	}
}
