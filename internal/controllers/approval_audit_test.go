package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"testing"
)

type failingApprovalAudit struct {
	*audit.MemoryRecorder
	failType string
	failed   bool
}

func (r *failingApprovalAudit) Append(ctx context.Context, event audit.Event) error {
	if event.Type == r.failType && !r.failed {
		r.failed = true
		return errors.New("temporary audit failure")
	}
	return r.MemoryRecorder.Append(ctx, event)
}
func TestApprovalAuditRecoversSubmissionAndAdmissionWrites(t *testing.T) {
	for _, eventType := range []string{"ApprovalDecisionSubmitted", "ApprovalDecisionAdmitted"} {
		for _, choice := range []v1alpha1.ApprovalChoice{v1alpha1.Approved, v1alpha1.Denied} {
			t.Run(eventType+string(choice), func(t *testing.T) {
				f := newApprovalFixture()
				f.decision.Spec.Decision = choice
				kube := f.kube(t, true)
				recorder := &failingApprovalAudit{MemoryRecorder: audit.NewMemoryRecorder(), failType: eventType}
				r := &ApprovalRequestReconciler{Client: kube, Audit: recorder}
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.request)}); err == nil {
					t.Fatal("expected transient audit error")
				}
				restarted := &ApprovalRequestReconciler{Client: kube, Audit: recorder}
				latest := reconcileApproval(t, restarted, f.request)
				if latest.Status.DecisionUID != f.decision.UID {
					t.Fatal("missing admitted decision UID")
				}
				for range 3 {
					reconcileApproval(t, restarted, f.request)
				}
				events := recorder.AllEvents()
				if len(events) != 2 {
					t.Fatalf("got %d audit events", len(events))
				}
				var submitted, admitted audit.Event
				for _, event := range events {
					if event.Type == "ApprovalDecisionSubmitted" {
						submitted = event
					} else {
						admitted = event
					}
				}
				if submitted.ID == "" || admitted.CausationID != submitted.ID || admitted.ID != audit.ApprovalAdmissionEventID(&latest) {
					t.Fatal("missing causal linkage")
				}
				var payload audit.ApprovalDecisionEvidence
				if err := json.Unmarshal(admitted.Data, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Choice != choice || payload.Approver.SubjectId != "alice" || payload.AdmittedAt == nil || !payload.AdmittedAt.Equal(latest.Status.CompletedAt.Time) || payload.SubmissionEvent != submitted.ID {
					t.Fatalf("incorrect evidence: %#v", payload)
				}
			})
		}
	}
}
