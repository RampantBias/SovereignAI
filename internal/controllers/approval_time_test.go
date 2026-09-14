package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestApprovalTimestampPersistenceAndReplay(t *testing.T) {
	for _, choice := range []v1alpha1.ApprovalChoice{v1alpha1.Approved, v1alpha1.Denied} {
		t.Run(string(choice), func(t *testing.T) {
			ctx := context.Background()
			f := newApprovalFixture()
			authored := v1alpha1.NewAuditTime(time.Date(2026, 9, 11, 12, 0, 0, 200123456, time.UTC))
			admitted := authored.Add(500 * time.Millisecond)
			f.decision.Spec.AuthoredAt = &authored
			f.decision.Spec.Decision = choice
			encoded, err := json.Marshal(f.decision)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, f.decision); err != nil {
				t.Fatal(err)
			}
			kube := f.kube(t, true)
			recorder := &failingApprovalAudit{MemoryRecorder: audit.NewMemoryRecorder(), failType: "ApprovalDecisionAdmitted"}
			r := &ApprovalRequestReconciler{Client: kube, Audit: recorder, Now: func() time.Time { return admitted }}
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.request)}); err == nil {
				t.Fatal("expected failed admission append")
			}
			var completed v1alpha1.ApprovalRequest
			if err := kube.Get(ctx, client.ObjectKeyFromObject(f.request), &completed); err != nil {
				t.Fatal(err)
			}
			encoded, err = json.Marshal(&completed)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &completed); err != nil {
				t.Fatal(err)
			}
			if err := kube.Status().Update(ctx, &completed); err != nil {
				t.Fatal(err)
			}
			restarted := &ApprovalRequestReconciler{Client: kube, Audit: recorder, Now: func() time.Time { return admitted.Add(time.Hour) }}
			for range 2 {
				reconcileApproval(t, restarted, f.request)
			}
			events := recorder.AllEvents()
			if len(events) != 2 {
				t.Fatalf("expected exactly two events, got %d", len(events))
			}
			for _, event := range events {
				want := authored.Time
				if event.Type == "ApprovalDecisionAdmitted" {
					want = admitted
				}
				if !event.OccurredAt.Equal(want) {
					t.Fatalf("%s time changed: %s", event.Type, event.OccurredAt)
				}
			}
		})
	}
}
