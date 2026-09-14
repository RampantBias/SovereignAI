package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRecoveryTimestampPersistenceAndReplay(t *testing.T) {
	ctx := context.Background()
	r, w, failed, recorder := recoveryAuditFixture(t)
	selectedAt := time.Date(2026, 9, 11, 12, 0, 0, 250123456, time.UTC)
	observedAt := selectedAt.Add(500 * time.Millisecond)
	r.Now = func() time.Time { return selectedAt }
	if err := r.beginWorkflowRetry(ctx, w, failed, "test-author"); err != nil {
		t.Fatal(err)
	}
	var updated v1alpha1.SovereignWorkflow
	roundTripStatus := func() {
		t.Helper()
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(w), &updated); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(&updated)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &updated); err != nil {
			t.Fatal(err)
		}
		if err := r.Status().Update(ctx, &updated); err != nil {
			t.Fatal(err)
		}
	}
	roundTripStatus()
	if !updated.Status.Refinement.Recovery.SelectedAt.Equal(selectedAt) {
		t.Fatal("selection lost precision")
	}
	frozen, _ := json.Marshal(updated.Status.Refinement.Recovery)
	r.Now = func() time.Time { return observedAt }
	recorder.failType = "StepAttemptRetried"
	if err := r.createAttempt(ctx, &updated, w.Spec.Steps[0], 1); err == nil {
		t.Fatal("expected failed audit append")
	}
	roundTripStatus()
	observation := updated.Status.Refinement.RetryObservation
	if observation == nil || !observation.ObservedAt.Equal(observedAt) {
		t.Fatal("observation not persisted at full precision")
	}
	var attempt v1alpha1.StepAttempt
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: w.Namespace, Name: observation.Attempt.Name}, &attempt); err != nil {
		t.Fatal(err)
	}
	if attempt.Status.ExecutionRef != nil {
		t.Fatal("published authority before recording retry")
	}
	restarted := &WorkflowReconciler{Client: r.Client, Reader: r.Reader, Scheme: r.Scheme, Audit: recorder, Now: func() time.Time { return observedAt.Add(time.Hour) }}
	if err := restarted.createAttempt(ctx, &updated, w.Spec.Steps[0], 1); err != nil {
		t.Fatal(err)
	}
	roundTripStatus()
	after, _ := json.Marshal(updated.Status.Refinement.Recovery)
	if string(after) != string(frozen) {
		t.Fatal("retry observation mutated the frozen decision")
	}
	var retryWrites []audit.Event
	for _, event := range recorder.attempted {
		if event.Type == "StepAttemptRetried" {
			retryWrites = append(retryWrites, event)
		}
		if event.Type == "RecoveryDecisionSelected" && !event.OccurredAt.Equal(selectedAt) {
			t.Fatal("selection time drifted")
		}
	}
	if len(retryWrites) != 2 {
		t.Fatalf("expected failed write and replay, got %d", len(retryWrites))
	}
	for _, event := range retryWrites {
		if !event.OccurredAt.Equal(observedAt) || !event.OccurredAt.After(selectedAt) {
			t.Fatal("replacement time truncated or drifted")
		}
		var payload audit.WorkflowRetryRecorded
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ObservedAt == nil || !payload.ObservedAt.Equal(observedAt) {
			t.Fatal("timestamp basis missing")
		}
	}
	first, _ := json.Marshal(retryWrites[0])
	second, _ := json.Marshal(retryWrites[1])
	if string(first) != string(second) {
		t.Fatal("replay changed retry evidence")
	}
	var run v1alpha1.AgentRun
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: w.Namespace, Name: observation.Execution.Name}, &run); err != nil {
		t.Fatal(err)
	}
	run.UID = "different-execution"
	if _, err := r.persistRetryObservation(ctx, &updated, &attempt, &run); err == nil {
		t.Fatal("reused observation for a different execution")
	}
}
