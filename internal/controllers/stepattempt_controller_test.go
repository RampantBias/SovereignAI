package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAgentAttemptCreatesRestrictedPod(t *testing.T) {
	scheme := attemptScheme(t)
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: "wf"},
		Spec:       v1alpha1.SovereignWorkflowSpec{WorkflowID: "wf"},
		Status:     v1alpha1.SovereignWorkflowStatus{PvcName: "wf-workspace"},
	}
	attempt := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: "architect-001", Namespace: "wf", Labels: map[string]string{LabelWorkflow: "wf"}},
		Spec: v1alpha1.StepAttemptSpec{WorkflowRef: "wf", StepName: "architect", Attempt: 1, Kind: v1alpha1.ExecutionKindAgent,
			Goal: "plan", Image: "agent@sha256:test", Executable: []string{"/domain-agent", "--role", "architect"},
			OutputContracts: []v1alpha1.ContractReference{{Name: "implementation-plan", Version: "v1"}}},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.StepAttempt{}).
		WithObjects(workflow, attempt).Build()
	reconciler := &StepAttemptReconciler{Client: client, Scheme: scheme}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "wf", Name: attempt.Name}}
	for range 2 {
		if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	var pod corev1.Pod
	if err := client.Get(context.Background(), request.NamespacedName, &pod); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("agent pod must not automount a service-account token")
	}
	security := pod.Spec.Containers[0].SecurityContext
	if security == nil || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation {
		t.Fatal("agent pod permits privilege escalation")
	}
	if security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem {
		t.Fatal("agent root filesystem is writable")
	}
	if len(security.Capabilities.Drop) != 1 || security.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("agent capabilities were not dropped: %#v", security.Capabilities)
	}
	var input corev1.ConfigMap
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: "wf", Name: attempt.Name + "-input"}, &input); err != nil {
		t.Fatal(err)
	}
	if input.Data["input.json"] == "" {
		t.Fatal("input contract was not generated")
	}
	var contract agentcontract.Input
	if err := json.Unmarshal([]byte(input.Data["input.json"]), &contract); err != nil {
		t.Fatal(err)
	}
	if len(contract.Outputs) != 1 {
		t.Fatalf("output obligations = %#v, want one obligation", contract.Outputs)
	}
	if contract.Outputs[0].Name != "implementation-plan" || contract.Outputs[0].Version != "v1" {
		t.Fatalf("unexpected output obligation: %#v", contract.Outputs[0])
	}
	if !contract.Outputs[0].Required {
		t.Fatalf("workflow output obligation should be required: %#v", contract.Outputs[0])
	}
	if contract.ControlPath != "/workspace/attempts/architect-001/control" {
		t.Fatalf("controlPath = %q", contract.ControlPath)
	}
	if contract.ResultPath != "/workspace/attempts/architect-001/control/result.json" {
		t.Fatalf("resultPath = %q", contract.ResultPath)
	}
	if contract.AuditEventsPath != "/workspace/attempts/architect-001/control/events.jsonl" {
		t.Fatalf("auditEventsPath = %q", contract.AuditEventsPath)
	}
}

func TestUtilityAttemptCreatesNonRetryingJob(t *testing.T) {
	scheme := attemptScheme(t)
	attempt := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: "tests-001", Namespace: "wf"},
		Spec: v1alpha1.StepAttemptSpec{WorkflowRef: "wf", StepName: "tests", Attempt: 1, Kind: v1alpha1.ExecutionKindUtility,
			Goal: "test", Image: "utility@sha256:test", Executable: []string{"/utility", "test"}},
		Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhasePending},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.StepAttempt{}).WithObjects(attempt).Build()
	recorder := audit.NewMemoryRecorder()
	reconciler := &StepAttemptReconciler{Client: client, Scheme: scheme, Audit: recorder}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "wf", Name: attempt.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var job batchv1.Job
	if err := client.Get(context.Background(), request.NamespacedName, &job); err != nil {
		t.Fatal(err)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Fatalf("utility job backoff = %v, want 0", job.Spec.BackoffLimit)
	}
	if !recorder.Has("UtilityJobCreated") {
		t.Fatalf("expected UtilityJobCreated audit event, got %#v", recorder.AllEvents())
	}
}

func TestCollectorJobReceivesRuntimeAuditPath(t *testing.T) {
	t.Setenv("SOVEREIGN_AUDIT_DSN", "postgres://audit")
	attempt := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: "architect-001", Namespace: "wf"},
		Spec:       v1alpha1.StepAttemptSpec{WorkflowRef: "wf"},
	}
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: "wf"},
		Status:     v1alpha1.SovereignWorkflowStatus{PvcName: "wf-workspace"},
	}
	objects := buildCollectorResources(attempt, workflow, "collector:test")
	var job *batchv1.Job
	for _, object := range objects {
		if candidate, ok := object.(*batchv1.Job); ok {
			job = candidate
			break
		}
	}
	if job == nil {
		t.Fatal("collector job was not created")
	}
	container := job.Spec.Template.Spec.Containers[0]
	if !containsArgPair(container.Args, "--audit-events", "/workspace/attempts/architect-001/control/events.jsonl") {
		t.Fatalf("collector args do not include runtime audit path: %#v", container.Args)
	}
	if !containsEnv(container.Env, "SOVEREIGN_AUDIT_DSN", "postgres://audit") {
		t.Fatalf("collector env does not include audit DSN: %#v", container.Env)
	}
}

func TestAgentAttemptMarksMissingPodInterrupted(t *testing.T) {
	scheme := attemptScheme(t)
	attempt := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: "developer-001", Namespace: "wf", Labels: map[string]string{LabelWorkflow: "wf"}},
		Spec: v1alpha1.StepAttemptSpec{
			WorkflowRef: "wf",
			StepName:    "developer",
			Attempt:     1,
			Kind:        v1alpha1.ExecutionKindAgent,
			Image:       "agent@sha256:test",
		},
		Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhaseRunning, PodRef: "developer-001"},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.StepAttempt{}).WithObjects(attempt).Build()
	recorder := audit.NewMemoryRecorder()
	reconciler := &StepAttemptReconciler{Client: client, Scheme: scheme, Audit: recorder}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "wf", Name: attempt.Name}}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	var updated v1alpha1.StepAttempt
	if err := client.Get(context.Background(), request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != v1alpha1.PhaseInterrupted {
		t.Fatalf("phase = %s, want %s", updated.Status.Phase, v1alpha1.PhaseInterrupted)
	}
	if !updated.Status.Retryable || updated.Status.FailureReason != "AgentPodLost" {
		t.Fatalf("unexpected retry state: retryable=%v reason=%q", updated.Status.Retryable, updated.Status.FailureReason)
	}
	if !recorder.Has("StepAttemptInterrupted") {
		t.Fatalf("expected StepAttemptInterrupted audit event, got %#v", recorder.AllEvents())
	}
}

func TestAgentAttemptDeletesPodWhenInferenceLeaseInterrupted(t *testing.T) {
	scheme := attemptScheme(t)
	attempt := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: "developer-001", Namespace: "wf", Labels: map[string]string{LabelWorkflow: "wf"}},
		Spec: v1alpha1.StepAttemptSpec{
			WorkflowRef:       "wf",
			StepName:          "developer",
			Attempt:           1,
			Kind:              v1alpha1.ExecutionKindAgent,
			Image:             "agent@sha256:test",
			Inference:         &v1alpha1.InferenceRequestSpec{},
			InferenceLeaseRef: "inference-001",
		},
		Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhaseRunning, PodRef: "developer-001"},
	}
	lease := &v1alpha1.InferenceLease{
		ObjectMeta: metav1.ObjectMeta{Name: "inference-001", Namespace: "wf"},
		Status:     v1alpha1.InferenceLeaseStatus{Phase: v1alpha1.PhaseInterrupted, Reason: "EvictedByHigherPriorityLease"},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "developer-001", Namespace: "wf"}}

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.StepAttempt{}, &v1alpha1.InferenceLease{}).
		WithObjects(attempt, lease, pod).Build()

	recorder := audit.NewMemoryRecorder()
	reconciler := &StepAttemptReconciler{Client: client, Scheme: scheme, Audit: recorder}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "wf", Name: attempt.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	var updated v1alpha1.StepAttempt
	if err := client.Get(context.Background(), request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != v1alpha1.PhaseInterrupted {
		t.Fatalf("phase = %s, want %s", updated.Status.Phase, v1alpha1.PhaseInterrupted)
	}
	if !updated.Status.Retryable || updated.Status.FailureReason != "EvictedByHigherPriorityLease" {
		t.Fatalf("unexpected retry state: retryable=%v reason=%q", updated.Status.Retryable, updated.Status.FailureReason)
	}
	var deleted corev1.Pod
	err := client.Get(context.Background(), types.NamespacedName{Namespace: "wf", Name: "developer-001"}, &deleted)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("agent pod should be deleted after lease interruption, got err=%v pod=%#v", err, deleted)
	}
	if !recorder.Has("StepAttemptInterrupted") {
		t.Fatalf("expected StepAttemptInterrupted audit event, got %#v", recorder.AllEvents())
	}
}

func containsArgPair(args []string, name, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == name && args[i+1] == value {
			return true
		}
	}
	return false
}

func containsEnv(env []corev1.EnvVar, name, value string) bool {
	for _, item := range env {
		if item.Name == name && item.Value == value {
			return true
		}
	}
	return false
}

func attemptScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1alpha1.AddToScheme, corev1.AddToScheme, batchv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}
