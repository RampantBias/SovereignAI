package workflow

import (
	"context"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	workflow := changeRequestWorkflow()
	workflow.Spec.Steps = []v1alpha1.StepConfig{{
		Name: "architect", Kind: v1alpha1.ExecutionKindAgent,
		Agent: &v1alpha1.AgentStepSpec{Responsibility: "plan", Image: "agent", Executable: []string{"/agent"}},
	}}
	workflow.Status.Phase = string(v1alpha1.PhasePending)

	artifact := changeRequestArtifact(workflow)
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.StepAttempt{}, &v1alpha1.AgentRun{}, &v1alpha1.Artifact{}).
		WithObjects(workflow, artifact).Build()
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
	if len(attempts.Items) != 1 || attempts.Items[0].Spec.RetryNumber != 1 {
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

func TestWorkflowRetriesRetryableFailedAttempt(t *testing.T) {
	ctx := context.Background()
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
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	// take pending step workflow, add additional step,
	workflow := changeRequestWorkflow()
	workflow.Spec.Steps = []v1alpha1.StepConfig{{
		Name:        "test-author",
		Kind:        v1alpha1.ExecutionKindAgent,
		Order:       1,
		MaxAttempts: 2,
		Agent: &v1alpha1.AgentStepSpec{
			Responsibility: "write tests",
			Image:          "agent:test",
			Executable:     []string{"/reference-agent"},
		},
		Outputs: []v1alpha1.ContractReference{{
			Name:    "test-change-set",
			Version: "v1",
		}},
	}}
	workflow.Status.Phase = string(v1alpha1.PhaseRunning)
	workflow.Status.ActiveStepName = "test-author"
	workflow.Status.ActiveAttemptRef = "test-author-001"

	// fail step attempt to trigger retry
	controller := true
	failed := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-author-001",
			Namespace: workflow.Namespace,
			Labels: map[string]string{
				controllermeta.LabelWorkflow: workflow.Spec.WorkflowID,
				controllermeta.LabelStep:     "test-author",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.GroupVersion.String(), Kind: "SovereignWorkflow",
				Name: workflow.Name, UID: workflow.UID, Controller: &controller,
			}},
		},
		Spec: v1alpha1.StepAttemptSpec{
			Kind: v1alpha1.ExecutionKindAgent,
			WorkflowRef: v1alpha1.UIDReference{
				Name: workflow.Name,
				UID:  workflow.UID,
			},
			StepName:        "test-author",
			RetryNumber:     1,
			WorkflowAttempt: 1,
		},
		Status: v1alpha1.StepAttemptStatus{
			Phase:          v1alpha1.PhaseFailed,
			FailureReason:  "TestPathNotRecognized",
			FailureMessage: `test change set file "src/main.go" is not a recognized test path`,
			Retryable:      true,
		},
	}

	artifact := changeRequestArtifact(workflow)

	recorder := audit.NewMemoryRecorder()
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(
			&v1alpha1.SovereignWorkflow{},
			&v1alpha1.StepAttempt{},
			&v1alpha1.AgentRun{},
			&v1alpha1.Artifact{},
		).
		WithObjects(artifact, workflow, failed).
		Build()

	reconciler := &WorkflowReconciler{
		Client: kubeClient,
		Reader: kubeClient,
		Scheme: scheme,
		Audit:  recorder,
	}

	// 1 -> create workspace writer lease
	// 2 -> create test-author-002
	// 3 -> observe new attempt
	for range 3 {
		if _, err := reconciler.Reconcile(
			ctx,
			ctrl.Request{NamespacedName: client.ObjectKeyFromObject(workflow)}); err != nil {
			t.Fatal(err)
		}
	}

	// test-author-001 still exists and remains failed
	var original v1alpha1.StepAttempt
	if err := kubeClient.Get(ctx, types.NamespacedName{
		Namespace: workflow.Namespace,
		Name:      "test-author-001",
	}, &original); err != nil {
		t.Fatal(err)
	}
	if original.Status.Phase != v1alpha1.PhaseFailed || original.Spec.RetryNumber != 1 {
		t.Fatal("original attempt was modified")
	}

	// test-author-002 exists with matching attempt #
	var retried v1alpha1.StepAttempt
	if err := kubeClient.Get(ctx, types.NamespacedName{
		Namespace: workflow.Namespace,
		Name:      "test-author-002",
	}, &retried); err != nil {
		t.Fatal(err)
	}
	if retried.Spec.RetryNumber != 2 || retried.Spec.StepName != "test-author" {
		t.Fatalf("unexpected retry attempt: #%d; name: %s", retried.Spec.RetryNumber, retried.Spec.StepName)
	}

	// test-author-002 is owned by new attempt
	var retriedRun v1alpha1.AgentRun
	if err := kubeClient.Get(ctx, types.NamespacedName{
		Name:      "test-author-002",
		Namespace: workflow.Namespace,
	}, &retriedRun); err != nil {
		t.Fatal(err)
	}
	if retriedRun.Spec.AttemptRef != retried.Name ||
		retriedRun.Spec.Attempt != 2 ||
		!metav1.IsControlledBy(&retriedRun, &retried) ||
		retried.Status.ExecutionRef == nil ||
		retried.Status.ExecutionRef.Name != retriedRun.Name {
		t.Fatalf("unexpected owner and attempt on retry run: %s; %d", retriedRun.Spec.AttemptRef, retriedRun.Spec.Attempt)
	}
	if retriedRun.Spec.PriorAttemptRef == nil ||
		retriedRun.Spec.PriorAttemptRef.PreviousAttemptRef != failed.Name ||
		retriedRun.Spec.PriorAttemptRef.Code != failed.Status.FailureReason ||
		retriedRun.Spec.PriorAttemptRef.Message != failed.Status.FailureMessage {
		t.Fatalf("retry run did not preserve bounded prior-attempt feedback: %#v", retriedRun.Spec.PriorAttemptRef)
	}

	// auditing events
	if !recorder.Has("RecoveryDecisionSelected") {
		t.Error("missing RecoveryDecisionSelected audit event")
	}
	if !recorder.Has("StepAttemptRetried") {
		t.Error("missing StepAttemptRetried audit event")
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
		ObjectMeta: metav1.ObjectMeta{Name: "wf-terminating", Namespace: "wf-terminating", UID: "uid-1", Finalizers: []string{controllermeta.WorkflowFinalizer}},
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
