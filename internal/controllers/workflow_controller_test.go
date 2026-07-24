package controllers

import (
	"context"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	coordinationv1 "k8s.io/api/coordination/v1"
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
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf-1", Namespace: "wf-1", UID: "uid-1", Finalizers: []string{WorkflowFinalizer}},
		Spec: v1alpha1.SovereignWorkflowSpec{Project: v1alpha1.UIDReference{Name: "project"}, WorkflowID: "wf-1", Steps: []v1alpha1.StepConfig{{
			Name: "architect", Kind: v1alpha1.ExecutionKindAgent,
			Agent: &v1alpha1.AgentStepSpec{Responsibility: "plan", Image: "agent", Executable: []string{"/agent"}},
		}}},
		Status: v1alpha1.SovereignWorkflowStatus{Phase: string(v1alpha1.PhasePending)},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.StepAttempt{}, &v1alpha1.AgentRun{}).
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
	var runs v1alpha1.AgentRunList
	if err := client.List(context.Background(), &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 || attempts.Items[0].Status.ExecutionRef == nil || attempts.Items[0].Status.ExecutionRef.Kind != "AgentRun" {
		t.Fatalf("expected one owned AgentRun and a typed execution reference, runs=%#v attempt=%#v", runs.Items, attempts.Items[0].Status)
	}
}

func TestWorkflowSkipsNormalReconcileWhenNamespaceTerminating(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "wf-terminating",
			DeletionTimestamp: &now,
			Finalizers:        []string{"kubernetes"},
		},
	}
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf-terminating", Namespace: "wf-terminating", UID: "uid-1", Finalizers: []string{WorkflowFinalizer}},
		Spec: v1alpha1.SovereignWorkflowSpec{Project: v1alpha1.UIDReference{Name: "project"}, WorkflowID: "wf-terminating", Steps: []v1alpha1.StepConfig{{
			Name: "architect", Kind: v1alpha1.ExecutionKindAgent,
			Agent: &v1alpha1.AgentStepSpec{Responsibility: "plan", Image: "agent", Executable: []string{"/agent"}},
		}}},
		Status: v1alpha1.SovereignWorkflowStatus{Phase: string(v1alpha1.PhasePending)},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.StepAttempt{}, &v1alpha1.AgentRun{}).
		WithObjects(namespace, workflow).Build()
	reconciler := &WorkflowReconciler{Client: client, Scheme: scheme, Audit: audit.NewMemoryRecorder()}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: workflow.Name, Namespace: workflow.Namespace}}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	var attempts v1alpha1.StepAttemptList
	if err := client.List(context.Background(), &attempts); err != nil {
		t.Fatal(err)
	}
	if len(attempts.Items) != 0 {
		t.Fatalf("expected no attempts while namespace terminates, got %#v", attempts.Items)
	}
	var claims corev1.PersistentVolumeClaimList
	if err := client.List(context.Background(), &claims); err != nil {
		t.Fatal(err)
	}
	if len(claims.Items) != 0 {
		t.Fatalf("expected no PVCs while namespace terminates, got %#v", claims.Items)
	}
}

func TestWorkflowCreatesOneTypedDomainPrimitivePerAttemptKind(t *testing.T) {
	tests := []struct {
		name     string
		step     v1alpha1.StepConfig
		wantKind string
	}{
		{name: "agent", wantKind: "AgentRun", step: v1alpha1.StepConfig{Name: "work", Kind: v1alpha1.ExecutionKindAgent, Agent: &v1alpha1.AgentStepSpec{Responsibility: "work", Image: "agent", Executable: []string{"/agent"}}}},
		{name: "utility", wantKind: "UtilityOperation", step: v1alpha1.StepConfig{Name: "work", Kind: v1alpha1.ExecutionKindUtility, Utility: &v1alpha1.UtilityOperationRequest{Name: "test.run"}}},
		{name: "approval", wantKind: "ApprovalRequest", step: v1alpha1.StepConfig{Name: "work", Kind: v1alpha1.ExecutionKindHumanGate, Approval: &v1alpha1.ApprovalSpec{Mode: v1alpha1.AnyOf, RequiredGroups: []string{"maintainers"}, DenyBehavior: "Fail"}}},
		{name: "validation", wantKind: "ValidationRun", step: v1alpha1.StepConfig{Name: "work", Kind: v1alpha1.ExecutionKindValidation, Validation: &v1alpha1.ValidationStepSpec{Provider: "argocd-kustomize"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := v1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			workflow := &v1alpha1.SovereignWorkflow{
				ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: test.name, UID: types.UID("workflow-" + test.name)},
				Spec:       v1alpha1.SovereignWorkflowSpec{Project: v1alpha1.UIDReference{Name: "project"}, WorkflowID: "wf-" + test.name, Steps: []v1alpha1.StepConfig{test.step}},
			}
			client := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.StepAttempt{}, &v1alpha1.AgentRun{}, &v1alpha1.UtilityOperation{}, &v1alpha1.ApprovalRequest{}, &v1alpha1.ValidationRun{}).
				WithObjects(workflow).Build()
			reconciler := &WorkflowReconciler{Client: client, Scheme: scheme, Audit: audit.NewMemoryRecorder()}
			if err := reconciler.createAttempt(context.Background(), workflow, test.step, 1); err != nil {
				t.Fatal(err)
			}
			var attempt v1alpha1.StepAttempt
			if err := client.Get(context.Background(), types.NamespacedName{Namespace: workflow.Namespace, Name: "work-001"}, &attempt); err != nil {
				t.Fatal(err)
			}
			if attempt.Status.ExecutionRef == nil || attempt.Status.ExecutionRef.Kind != test.wantKind || attempt.Status.ExecutionRef.Name != attempt.Name {
				t.Fatalf("execution reference = %#v, want %s/%s", attempt.Status.ExecutionRef, test.wantKind, attempt.Name)
			}
			switch test.wantKind {
			case "AgentRun":
				var object v1alpha1.AgentRun
				if err := client.Get(context.Background(), types.NamespacedName{Namespace: workflow.Namespace, Name: attempt.Name}, &object); err != nil {
					t.Fatal(err)
				}
			case "UtilityOperation":
				var object v1alpha1.UtilityOperation
				if err := client.Get(context.Background(), types.NamespacedName{Namespace: workflow.Namespace, Name: attempt.Name}, &object); err != nil {
					t.Fatal(err)
				}
			case "ApprovalRequest":
				var object v1alpha1.ApprovalRequest
				if err := client.Get(context.Background(), types.NamespacedName{Namespace: workflow.Namespace, Name: attempt.Name}, &object); err != nil {
					t.Fatal(err)
				}
			case "ValidationRun":
				var object v1alpha1.ValidationRun
				if err := client.Get(context.Background(), types.NamespacedName{Namespace: workflow.Namespace, Name: attempt.Name}, &object); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
