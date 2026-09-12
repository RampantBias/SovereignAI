package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestAuditTimeSurvivesResourceSerialization(t *testing.T) {
	for _, value := range []string{"2026-09-11T12:00:00Z", "2026-09-11T12:00:00.987654321Z"} {
		t.Run(value, func(t *testing.T) {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				t.Fatal(err)
			}
			stamp := NewAuditTime(parsed)
			for _, resource := range []any{
				&ApprovalDecision{Spec: ApprovalDecisionSpec{AuthoredAt: &stamp}},
				&ApprovalRequest{Status: ApprovalRequestStatus{CompletedAt: &stamp}},
				&SovereignWorkflow{Status: SovereignWorkflowStatus{Refinement: &WorkflowRefinementStatus{
					Recovery:         &WorkflowRecoveryEvidence{SelectedAt: stamp},
					RetryObservation: &WorkflowRetryObservation{ObservedAt: stamp},
				}}},
			} {
				before, err := json.Marshal(resource)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(before), value) {
					t.Fatalf("timestamp lost: %s", before)
				}
				if err := json.Unmarshal(before, resource); err != nil {
					t.Fatal(err)
				}
				unstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(resource)
				if err != nil {
					t.Fatal(err)
				}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured, resource); err != nil {
					t.Fatal(err)
				}
				after, err := json.Marshal(resource)
				if err != nil {
					t.Fatal(err)
				}
				if string(before) != string(after) {
					t.Fatalf("timestamp changed during round trip:\n%s\n%s", before, after)
				}
			}
		})
	}
}
