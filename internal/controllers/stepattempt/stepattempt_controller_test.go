package stepattempt

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllers"
	policyengine "github.com/SovereignAI/internal/policy"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestStepAttemptMirrorsOwnedAgentRunStatus(t *testing.T) {
	scheme := attemptScheme(t)
	attempt := authorizedAttempt("architect-001", "wf", "architect", v1alpha1.ExecutionKindAgent)
	workflow := workflowFixture()
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: attempt.Name, Namespace: attempt.Namespace},
		Spec:       v1alpha1.AgentRunSpec{AttemptRef: attempt.Name, WorkflowRef: workflowRef(workflow), StepName: "architect", Attempt: 1, Responsibility: "plan", Image: "agent", Executable: []string{"/agent"}},
		Status:     v1alpha1.AgentRunStatus{Phase: v1alpha1.PhaseRunning},
	}
	ownByAttempt(run, attempt)
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.StepAttempt{}, &v1alpha1.AgentRun{}).
		WithObjects(attempt, run).Build()
	apiReader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(attempt, run).Build()
	reconciler := &StepAttemptReconciler{Client: client, Scheme: scheme, Reader: apiReader}
	if _, err := reconciler.Reconcile(context.Background(), requestFor(attempt)); err != nil {
		t.Fatal(err)
	}
	var updated v1alpha1.StepAttempt
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: attempt.Namespace, Name: attempt.Name}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != v1alpha1.PhaseRunning {
		t.Fatalf("attempt phase = %s, want Running", updated.Status.Phase)
	}
	if updated.Status.ExecutionRef == nil || updated.Status.ExecutionRef.Kind != "AgentRun" {
		t.Fatalf("execution reference was not preserved: %#v", updated.Status.ExecutionRef)
	}
}

func TestStepAttemptUsesAPIReaderForNewExecutionPrimitive(t *testing.T) {
	scheme := attemptScheme(t)
	attempt := authorizedAttempt("initialize-repository-w000-r001", "wf", "initialize-repository", v1alpha1.ExecutionKindUtility)
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: attempt.Name, Namespace: attempt.Namespace},
		Spec: v1alpha1.UtilityOperationSpec{
			AttemptRef:  attempt.Name,
			WorkflowRef: attempt.Spec.WorkflowRef,
			StepName:    attempt.Spec.StepName,
			Attempt:     attempt.Spec.RetryNumber,
			Operation:   v1alpha1.UtilityOperationRequest{Name: "repository.initialize"},
		},
		Status: v1alpha1.UtilityOperationStatus{Phase: v1alpha1.PhaseRunning},
	}
	ownByAttempt(operation, attempt)

	cachedClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.StepAttempt{}).
		WithObjects(attempt).Build()
	apiReader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(operation).Build()
	reconciler := &StepAttemptReconciler{
		Client: cachedClient,
		Reader: apiReader,
		Scheme: scheme,
	}

	if _, err := reconciler.Reconcile(context.Background(), requestFor(attempt)); err != nil {
		t.Fatal(err)
	}

	var updated v1alpha1.StepAttempt
	if err := cachedClient.Get(context.Background(), types.NamespacedName{
		Namespace: attempt.Namespace,
		Name:      attempt.Name,
	}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != v1alpha1.PhaseRunning {
		t.Fatalf("attempt phase = %s, want Running", updated.Status.Phase)
	}
	if updated.Status.FailureReason == "ExecutionPrimitiveLost" {
		t.Fatal("cache lag was treated as a lost execution primitive")
	}
}

func TestStepAttemptMirrorsOwnedAgentFailureDiagnostic(t *testing.T) {
	scheme := attemptScheme(t)
	attempt := authorizedAttempt("test-author-001", "wf", "test-author", v1alpha1.ExecutionKindAgent)
	workflow := workflowFixture()
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: attempt.Name, Namespace: attempt.Namespace},
		Spec:       v1alpha1.AgentRunSpec{AttemptRef: attempt.Name, WorkflowRef: workflowRef(workflow), StepName: "test-author", Attempt: 1, Responsibility: "write tests", Image: "agent", Executable: []string{"/agent"}},
		Status: v1alpha1.AgentRunStatus{
			Phase:          v1alpha1.PhaseFailed,
			FailureReason:  "TestPathNotRecognized",
			FailureMessage: `test change set file "src/main.go" is not a recognized test path`,
			Retryable:      true,
		},
	}
	ownByAttempt(run, attempt)
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.StepAttempt{}, &v1alpha1.AgentRun{}).
		WithObjects(attempt, run).Build()
	apiReader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(attempt, run).Build()
	reconciler := &StepAttemptReconciler{Client: client, Scheme: scheme, Reader: apiReader}
	if _, err := reconciler.Reconcile(context.Background(), requestFor(attempt)); err != nil {
		t.Fatal(err)
	}
	var updated v1alpha1.StepAttempt
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: attempt.Namespace, Name: attempt.Name}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.FailureReason != run.Status.FailureReason || updated.Status.FailureMessage != run.Status.FailureMessage || !updated.Status.Retryable {
		t.Fatalf("attempt did not preserve failure diagnostic: %#v", updated.Status)
	}
}

func TestApprovalRequestOwnsAwaitingApprovalState(t *testing.T) {
	scheme := attemptScheme(t)
	workflow := workflowFixture()
	attempt := authorizedAttempt("approval-001", "wf", "approval", v1alpha1.ExecutionKindHumanGate)
	approval := &v1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "approval-001", Namespace: "wf", UID: "approval-request-uid"},
		Spec: v1alpha1.ApprovalRequestSpec{AttemptRef: v1alpha1.UIDReference{Name: "approval-001", UID: attempt.UID}, WorkflowRef: workflowRef(workflow), StepName: "approval", Attempt: 1,
			Approval: v1alpha1.ApprovalSpec{Mode: v1alpha1.AnyOf, RequiredGroups: []string{"maintainers"}, DenyBehavior: "Fail"}},
	}
	ownByAttempt(approval, attempt)
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.ApprovalRequest{}).WithObjects(attempt, approval).Build()
	recorder := audit.NewMemoryRecorder()
	reconciler := &controllers.ApprovalRequestReconciler{Client: client, Audit: recorder}
	if _, err := reconciler.Reconcile(context.Background(), requestFor(approval)); err != nil {
		t.Fatal(err)
	}
	var updated v1alpha1.ApprovalRequest
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: approval.Namespace, Name: approval.Name}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != v1alpha1.PhaseAwaitingApproval {
		t.Fatalf("approval request phase = %s", updated.Status.Phase)
	}
	var resolved audit.Event
	for _, event := range recorder.AllEvents() {
		if event.Type == "InputsResolved" {
			resolved = event
			break
		}
	}
	if resolved.ID == "" {
		t.Fatal("ApprovalRequest did not record InputsResolved before awaiting approval")
	}
	var inputs audit.InputsResolved
	if err := json.Unmarshal(resolved.Data, &inputs); err != nil {
		t.Fatalf("decode ApprovalRequest InputsResolved payload: %v", err)
	}
	if inputs.SchemaVersion != audit.PayloadSchemaVersionV1 || inputs.Consumer.Kind != "ApprovalRequest" ||
		inputs.Consumer.UID != string(approval.UID) || len(inputs.Inputs) != 0 {
		t.Fatalf("unexpected ApprovalRequest InputsResolved payload: %#v", inputs)
	}
}

func attemptScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func workflowFixture() *v1alpha1.SovereignWorkflow {
	return &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: "wf", UID: "workflow-uid"},
		Spec:       v1alpha1.SovereignWorkflowSpec{WorkflowID: "wf", Project: v1alpha1.UIDReference{Name: "project", UID: "project-uid"}},
		Status:     v1alpha1.SovereignWorkflowStatus{PvcName: "wf-workspace", WorkspaceWriterLeaseRef: "wf-workspace-writer"},
	}
}

func workspaceLeaseFixture() *coordinationv1.Lease {
	zero := int32(0)
	controller := true
	return &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Name: "wf-workspace-writer", Namespace: "wf",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "SovereignWorkflow", Name: "wf", UID: "workflow-uid", Controller: &controller}},
	}, Spec: coordinationv1.LeaseSpec{LeaseTransitions: &zero}}
}

func projectFixture() *v1alpha1.SovereignProject {
	return &v1alpha1.SovereignProject{ObjectMeta: metav1.ObjectMeta{Name: "project"}, Spec: v1alpha1.SovereignProjectSpec{
		ApplicationRepository: v1alpha1.RepositorySpec{URL: "https://example.test/repository.git", DefaultRevision: "main"},
		PolicyProfileRef:      "default", TestJob: v1alpha1.JobTemplateSpec{Image: "test-tools:dev", Command: []string{"go", "test"}},
		BuildJob: v1alpha1.JobTemplateSpec{Image: "build-tools:dev", Command: []string{"build"}},
	}}
}

func requestFor(object metav1.Object) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: object.GetNamespace(), Name: object.GetName()}}
}

type allowPolicy struct{}

func (allowPolicy) Evaluate(context.Context, any) (policyengine.Decision, error) {
	return policyengine.Decision{ID: "allow-test", Allowed: true}, nil
}

func authorizedAttempt(name, namespace, step string, kind v1alpha1.ExecutionKind) *v1alpha1.StepAttempt {
	return &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID("uid-" + name)},
		Spec:       v1alpha1.StepAttemptSpec{WorkflowRef: v1alpha1.UIDReference{Name: "wf", UID: "workflow-uid"}, StepName: step, RetryNumber: 1, Kind: kind, WorkflowAttempt: 1},
		Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhasePending, ExecutionRef: &v1alpha1.TypedLocalReference{
			APIVersion: v1alpha1.GroupVersion.String(), Kind: controllers.DomainKind(kind), Name: name,
		}},
	}
}

func ownByAttempt(object metav1.Object, attempt *v1alpha1.StepAttempt) {
	object.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(attempt, v1alpha1.GroupVersion.WithKind("StepAttempt"))})
}

func workflowRef(workflow *v1alpha1.SovereignWorkflow) v1alpha1.UIDReference {
	return v1alpha1.UIDReference{
		Name: workflow.Name,
		UID:  types.UID(workflow.UID),
	}
}
