package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/inference"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestInferenceLeaseBindsCompatibleWarmEndpoint(t *testing.T) {
	scheme := inferenceScheme(t)
	endpoint := &v1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "warm", Namespace: InferenceNamespace, Labels: map[string]string{"sovereign-ai.io/project": "project"}},
		Spec:       v1alpha1.InferenceEndpointSpec{Model: "code", ModelRevision: "v1", Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject, MaxKVRAMMiB: 10000, SafetyHeadroomMiB: 1000},
		Status:     v1alpha1.InferenceEndpointStatus{Phase: v1alpha1.PhaseRunning},
	}
	lease := &v1alpha1.InferenceLease{
		ObjectMeta: metav1.ObjectMeta{Name: "lease", Namespace: "workflow"},
		Spec:       v1alpha1.InferenceLeaseSpec{WorkflowRef: "wf", AttemptRef: "developer-001", ProjectRef: "project", Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject, Model: "code", ModelRevision: "v1", EstimatedKVRAMMiB: 2000},
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
