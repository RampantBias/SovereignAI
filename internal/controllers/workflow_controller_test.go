package controllers

import (
	"context"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWorkflowCreatesFirstAttemptIdempotently(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf-1", Namespace: "wf-1", UID: "uid-1", Finalizers: []string{WorkflowFinalizer}},
		Spec:       v1alpha1.SovereignWorkflowSpec{ProjectName: "project", WorkflowID: "wf-1", Steps: []v1alpha1.StepConfig{{Name: "architect", Kind: v1alpha1.ExecutionKindAgent, Responsibility: "plan"}}},
		Status:     v1alpha1.SovereignWorkflowStatus{Phase: string(v1alpha1.PhasePending)},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.StepAttempt{}).
		WithObjects(workflow).Build()
	reconciler := &WorkflowReconciler{Client: client, Scheme: scheme, Audit: audit.NewMemoryRecorder()}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: workflow.Name, Namespace: workflow.Namespace}}
	for range 3 {
		if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	var attempts v1alpha1.StepAttemptList
	if err := client.List(context.Background(), &attempts); err != nil {
		t.Fatal(err)
	}
	if len(attempts.Items) != 1 || attempts.Items[0].Spec.Attempt != 1 {
		t.Fatalf("expected one attempt, got %#v", attempts.Items)
	}
	var updated v1alpha1.SovereignWorkflow
	if err := client.Get(context.Background(), request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.ActiveAttemptRef != attempts.Items[0].Name {
		t.Fatalf("workflow points to %q, attempt is %q", updated.Status.ActiveAttemptRef, attempts.Items[0].Name)
	}
}
