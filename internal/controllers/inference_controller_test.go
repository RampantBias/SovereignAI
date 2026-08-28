package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/inference"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTerminalAgentRunReleasesInferenceCapacityForNextAttempt(t *testing.T) {
	ctx := context.Background()
	scheme := inferenceScheme(t)
	workflowNamespace := "workflow"
	endpoint := &v1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "warm", Namespace: InferenceNamespace, Labels: map[string]string{"sovereign-ai.io/project": "project"}},
		Spec: v1alpha1.InferenceEndpointSpec{
			Model: "code", ModelRevision: "v1", Tenant: "team", Classification: "internal",
			SharingScope: v1alpha1.SharingWithinProject, MaxKVRAMMiB: 8192, SafetyHeadroomMiB: 1024,
		},
		Status: v1alpha1.InferenceEndpointStatus{
			Phase:             v1alpha1.PhaseRunning,
			AllocatedKVRAMMiB: 6144,
			ActiveLeaseCount:  3,
			ActiveLeases: []v1alpha1.NamespacedReference{
				{Namespace: workflowNamespace, Name: "architect-001-inference"},
				{Namespace: workflowNamespace, Name: "test-author-001-inference"},
				{Namespace: workflowNamespace, Name: "test-author-002-inference"},
			},
		},
	}
	completedLease := &v1alpha1.InferenceLease{
		ObjectMeta: metav1.ObjectMeta{Name: "test-author-002-inference", Namespace: workflowNamespace},
		Spec: v1alpha1.InferenceLeaseSpec{
			WorkflowRef: v1alpha1.UIDReference{Name: "wf"}, AttemptRef: "test-author-002", ProjectRef: "project",
			Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject,
			Model: "code", ModelRevision: "v1", EstimatedKVRAMMiB: 2048,
		},
		Status: v1alpha1.InferenceLeaseStatus{
			Phase:       v1alpha1.PhaseRunning,
			EndpointRef: v1alpha1.NamespacedReference{Namespace: endpoint.Namespace, Name: endpoint.Name},
			EndpointURL: "http://warm.sovereign-inference.svc:8000",
		},
	}
	nextLease := &v1alpha1.InferenceLease{
		ObjectMeta: metav1.ObjectMeta{Name: "test-author-003-inference", Namespace: workflowNamespace},
		Spec: v1alpha1.InferenceLeaseSpec{
			WorkflowRef: v1alpha1.UIDReference{Name: "wf"}, AttemptRef: "test-author-003", ProjectRef: "project",
			Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject,
			Model: "code", ModelRevision: "v1", EstimatedKVRAMMiB: 2048,
		},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: InferenceNamespace}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.InferenceLease{}, &v1alpha1.InferenceEndpoint{}).
		WithObjects(namespace, endpoint, completedLease, nextLease).Build()
	recorder := audit.NewMemoryRecorder()
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "test-author-002", Namespace: workflowNamespace},
		Spec: v1alpha1.AgentRunSpec{
			WorkflowRef: v1alpha1.UIDReference{Name: "wf"}, StepName: "test-author", Attempt: 2,
		},
		Status: v1alpha1.AgentRunStatus{
			Phase: v1alpha1.PhaseFailed, InferenceLeaseRef: completedLease.Name,
		},
	}
	agentReconciler := &AgentRunReconciler{Client: kubeClient, Scheme: scheme, Audit: recorder}
	released, err := agentReconciler.reconcileInferenceLeaseRelease(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if released {
		t.Fatal("terminal AgentRun reported release before the InferenceLease acknowledged it")
	}
	var requested v1alpha1.InferenceLease
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: completedLease.Namespace, Name: completedLease.Name}, &requested); err != nil {
		t.Fatal(err)
	}
	if requested.Annotations[InferenceLeaseReleaseRequestAnnotation] != "AgentRunFailed" {
		t.Fatalf("release request annotation = %q", requested.Annotations[InferenceLeaseReleaseRequestAnnotation])
	}

	leaseReconciler := &InferenceLeaseReconciler{Client: kubeClient, Scheme: scheme, Audit: recorder}
	completedRequest := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: completedLease.Namespace, Name: completedLease.Name}}
	if _, err := leaseReconciler.Reconcile(ctx, completedRequest); err != nil {
		t.Fatal(err)
	}
	var releasedLease v1alpha1.InferenceLease
	if err := kubeClient.Get(ctx, completedRequest.NamespacedName, &releasedLease); err != nil {
		t.Fatal(err)
	}
	if releasedLease.Status.Phase != v1alpha1.PhaseSucceeded || releasedLease.Status.Reason != "AgentRunFailed" {
		t.Fatalf("lease release was not acknowledged: %#v", releasedLease.Status)
	}
	var releasedEndpoint v1alpha1.InferenceEndpoint
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: endpoint.Namespace, Name: endpoint.Name}, &releasedEndpoint); err != nil {
		t.Fatal(err)
	}
	if releasedEndpoint.Status.AllocatedKVRAMMiB != 4096 || releasedEndpoint.Status.ActiveLeaseCount != 2 ||
		containsLeaseReference(releasedEndpoint.Status.ActiveLeases, completedLease.Namespace, completedLease.Name) {
		t.Fatalf("endpoint reservation was not released: %#v", releasedEndpoint.Status)
	}
	released, err = agentReconciler.reconcileInferenceLeaseRelease(ctx, run)
	if err != nil || !released {
		t.Fatalf("terminal AgentRun did not observe release acknowledgement: released=%t err=%v", released, err)
	}
	if _, err := leaseReconciler.Reconcile(ctx, completedRequest); err != nil {
		t.Fatal(err)
	}

	nextRequest := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: nextLease.Namespace, Name: nextLease.Name}}
	for range 2 {
		if _, err := leaseReconciler.Reconcile(ctx, nextRequest); err != nil {
			t.Fatal(err)
		}
	}
	var bound v1alpha1.InferenceLease
	if err := kubeClient.Get(ctx, nextRequest.NamespacedName, &bound); err != nil {
		t.Fatal(err)
	}
	if bound.Status.Phase != v1alpha1.PhaseRunning || bound.Status.EndpointRef.Name != endpoint.Name {
		t.Fatalf("next attempt lease did not bind after release: %#v", bound.Status)
	}
	var reboundEndpoint v1alpha1.InferenceEndpoint
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: endpoint.Namespace, Name: endpoint.Name}, &reboundEndpoint); err != nil {
		t.Fatal(err)
	}
	if reboundEndpoint.Status.AllocatedKVRAMMiB != 6144 || reboundEndpoint.Status.ActiveLeaseCount != 3 {
		t.Fatalf("endpoint capacity was not reassigned exactly once: %#v", reboundEndpoint.Status)
	}
	for _, eventType := range []string{"InferenceLeaseReleaseRequested", "InferenceLeaseReleased", "InferenceLeaseBound"} {
		if !recorder.Has(eventType) {
			t.Errorf("missing %s audit event", eventType)
		}
	}
}

func containsLeaseReference(references []v1alpha1.NamespacedReference, namespace, name string) bool {
	for _, reference := range references {
		if reference.Namespace == namespace && reference.Name == name {
			return true
		}
	}
	return false
}

func TestInferenceLeaseBindsCompatibleWarmEndpoint(t *testing.T) {
	scheme := inferenceScheme(t)
	endpoint := &v1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "warm", Namespace: InferenceNamespace, Labels: map[string]string{"sovereign-ai.io/project": "project"}},
		Spec:       v1alpha1.InferenceEndpointSpec{Model: "code", ModelRevision: "v1", Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject, MaxKVRAMMiB: 10000, SafetyHeadroomMiB: 1000},
		Status:     v1alpha1.InferenceEndpointStatus{Phase: v1alpha1.PhaseRunning},
	}
	lease := &v1alpha1.InferenceLease{
		ObjectMeta: metav1.ObjectMeta{Name: "lease", Namespace: "workflow"},
		Spec:       v1alpha1.InferenceLeaseSpec{WorkflowRef: v1alpha1.UIDReference{Name: "wf"}, AttemptRef: "developer-001", ProjectRef: "project", Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject, Model: "code", ModelRevision: "v1", EstimatedKVRAMMiB: 2000},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: InferenceNamespace}}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.InferenceLease{}, &v1alpha1.InferenceEndpoint{}).
		WithObjects(namespace, endpoint, lease).Build()
	reconciler := &InferenceLeaseReconciler{Client: client, Scheme: scheme}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: lease.Namespace, Name: lease.Name}}
	for range 2 {
		if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	var updatedLease v1alpha1.InferenceLease
	if err := client.Get(context.Background(), request.NamespacedName, &updatedLease); err != nil {
		t.Fatal(err)
	}
	if updatedLease.Status.Phase != v1alpha1.PhaseRunning || updatedLease.Status.EndpointRef.Name != endpoint.Name {
		t.Fatalf("lease was not bound: %#v", updatedLease.Status)
	}
	var updatedEndpoint v1alpha1.InferenceEndpoint
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: endpoint.Namespace, Name: endpoint.Name}, &updatedEndpoint); err != nil {
		t.Fatal(err)
	}
	if updatedEndpoint.Status.AllocatedKVRAMMiB != 2000 || len(updatedEndpoint.Status.ActiveLeases) != 1 {
		t.Fatalf("endpoint reservation is wrong: %#v", updatedEndpoint.Status)
	}
}

func TestInferenceWorkloadUsesKubernetesGPUPlacement(t *testing.T) {
	reconciler := InferenceEndpointReconciler{
		Profile: testInferenceProfile(),
	}
	endpoint := &v1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "endpoint", Namespace: InferenceNamespace},
		Spec:       v1alpha1.InferenceEndpointSpec{Model: "code", ModelRevision: "v1", RuntimeImage: "vllm@sha256:test"},
	}
	pod, service := reconciler.buildInferenceWorkloads(endpoint)
	if pod.Spec.NodeName != "" {
		t.Fatalf("controller pinned inference to node %q", pod.Spec.NodeName)
	}
	for _, variable := range pod.Spec.Containers[0].Env {
		if variable.Name == "CUDA_VISIBLE_DEVICES" {
			t.Fatal("controller manually selected a GPU device")
		}
	}
	autoToolChoice, toolCallParser := false, ""
	for index, argument := range pod.Spec.Containers[0].Args {
		switch argument {
		case "--enable-auto-tool-choice":
			autoToolChoice = true
		case "--tool-call-parser":
			if index+1 < len(pod.Spec.Containers[0].Args) {
				toolCallParser = pod.Spec.Containers[0].Args[index+1]
			}
		case "--structured-outputs-config":
			t.Fatal("inference runtime still configures obsolete structured chat outputs")
		}
	}
	if !autoToolChoice || toolCallParser != "hermes" {
		t.Fatalf("tool calling args = %v, want automatic Hermes parsing", pod.Spec.Containers[0].Args)
	}
	if got := pod.Spec.Containers[0].Resources.Limits["nvidia.com/gpu"]; got.String() != "1" {
		t.Fatalf("GPU request = %s, want 1", got.String())
	}
	if service.Spec.Selector["sovereign-ai.io/endpoint-id"] != endpoint.Name || pod.Labels["sovereign-ai.io/endpoint-id"] != endpoint.Name {
		t.Fatal("service selector does not match endpoint pod")
	}
}

func inferenceScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func testInferenceProfile() inference.Profile {
	return inference.Profile{
		RuntimeImage:      "docker.io/vllm/vllm-openai@sha256:770fe65b2c73ee74a5c42165cf3433de4048cc2cd9c57a937ca4e35aba5aa87b",
		ModelID:           "Qwen/Qwen2.5-Coder-3B-Instruct",
		ModelRevision:     "488639f1ff808d1d3d0ba301aef8c11461451ec5",
		ServedModelName:   "code-small",
		CachePVCName:      "sovereign-model-cache",
		CachePath:         "/model-cache",
		GPUNodeLabelKey:   "sovereign-ai.io/gpu-node",
		GPUNodeLabelValue: "true",
		StartupTimeout:    time.Minute * 15,
		RequestTimeout:    time.Minute * 3,
		MaxOutputTokens:   2048,
		MaxResponseBytes:  4194304, // 4 MB
	}
}
