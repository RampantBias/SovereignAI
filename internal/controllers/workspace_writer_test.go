package controllers

import (
	"context"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWorkspaceWriterLeaseIsExclusiveAndEpochIsMonotonic(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	controller := true
	workflow := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: "wf", UID: types.UID("workflow-uid")}}
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "wf-workspace-writer", Namespace: "wf", OwnerReferences: []metav1.OwnerReference{{Name: "wf", UID: workflow.UID, Controller: &controller}}},
		Spec:       coordinationv1.LeaseSpec{LeaseTransitions: &zero},
	}
	first := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "agent-001", Namespace: "wf", UID: types.UID("agent-uid")}}
	second := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "utility-001", Namespace: "wf", UID: types.UID("utility-uid")}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(lease).Build()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	firstGrant, state, err := acquireWorkspaceWriter(context.Background(), c, workflow, lease.Name, "AgentRun", first, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if state != workspaceWriterGranted || firstGrant.Epoch != 1 {
		t.Fatalf("first acquisition = %s epoch %d, want Granted epoch 1", state, firstGrant.Epoch)
	}

	_, state, err = acquireWorkspaceWriter(context.Background(), c, workflow, lease.Name, "UtilityOperation", second, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if state != workspaceWriterBlocked {
		t.Fatalf("second writer state = %s, want Blocked", state)
	}

	if err := releaseWorkspaceWriter(context.Background(), c, "wf", firstGrant, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	secondGrant, state, err := acquireWorkspaceWriter(context.Background(), c, workflow, lease.Name, "UtilityOperation", second, 0, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if state != workspaceWriterGranted || secondGrant.Epoch != 2 {
		t.Fatalf("second acquisition = %s epoch %d, want Granted epoch 2", state, secondGrant.Epoch)
	}

	_, state, err = acquireWorkspaceWriter(context.Background(), c, workflow, lease.Name, "AgentRun", first, firstGrant.Epoch, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if state != workspaceWriterLost {
		t.Fatalf("stale writer state = %s, want Lost", state)
	}
}
