package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/inference"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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
		ObjectMeta: metav1.ObjectMeta{Name: "warm", Namespace: controllermeta.InferenceNamespace, Labels: map[string]string{"sovereign-ai.io/project": "project"}},
		Spec:       v1alpha1.InferenceEndpointSpec{Model: "code", ModelRevision: "v1", Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject, MaxKVRAMMiB: 10000, SafetyHeadroomMiB: 1000},
		Status:     v1alpha1.InferenceEndpointStatus{Phase: v1alpha1.PhaseRunning},
	}
	lease := &v1alpha1.InferenceLease{
		ObjectMeta: metav1.ObjectMeta{Name: "lease", Namespace: "workflow", UID: "lease-uid"},
		Spec:       v1alpha1.InferenceLeaseSpec{WorkflowRef: v1alpha1.UIDReference{Name: "wf"}, AttemptRef: "developer-001", ProjectRef: "project", Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject, Model: "code", ModelRevision: "v1", EstimatedKVRAMMiB: 2000},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: controllermeta.InferenceNamespace}}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.InferenceLease{}, &v1alpha1.InferenceEndpoint{}).
		WithObjects(namespace, endpoint, lease).Build()
	recorder := audit.NewMemoryRecorder()
	reconciler := &InferenceLeaseReconciler{Client: client, Scheme: scheme, Audit: recorder}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: lease.Namespace, Name: lease.Name}}
	for range 3 {
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
	var admitted audit.Event
	admittedCount := 0
	for _, event := range recorder.AllEvents() {
		if event.Type == "InferenceLeaseAdmitted" {
			admitted = event
			admittedCount++
		}
	}
	if admitted.ID == "" {
		t.Fatal("InferenceLeaseAdmitted event was not recorded")
	}
	if admittedCount != 1 {
		t.Fatalf("replaying Running inference admission recorded %d events, want one", admittedCount)
	}
	var decision audit.DecisionEvaluated
	if err := json.Unmarshal(admitted.Data, &decision); err != nil {
		t.Fatalf("decode InferenceLeaseAdmitted payload: %v", err)
	}
	if decision.SchemaVersion != audit.PayloadSchemaVersionV1 || decision.Primitive.Kind != "InferenceLease" ||
		decision.Primitive.UID != string(lease.UID) || decision.Decision.ID == "" ||
		decision.Decision.Kind != "scheduler" || decision.Decision.Revision != "development-no-policy" ||
		decision.Decision.InputDigest == "" || decision.Decision.Outcome != "selected" ||
		admitted.DecisionID != decision.Decision.ID || len(decision.Invariants) != 3 {
		t.Fatalf("unexpected InferenceLeaseAdmitted decision: event=%#v payload=%#v", admitted, decision)
	}
	for _, invariant := range decision.Invariants {
		if invariant.Outcome != "passed" || invariant.Expected != invariant.Observed {
			t.Fatalf("InferenceLeaseAdmitted invariant did not pass: %#v", invariant)
		}
	}
}

func TestInferenceLeaseBindingPrunesMissingReservation(t *testing.T) {
	scheme := inferenceScheme(t)
	ghost := v1alpha1.NamespacedReference{Namespace: "deleted-workflow", Name: "developer-inference"}
	warmEndpointName := endpointName("code", "v1", "team", "internal")
	endpoint := &v1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: warmEndpointName, Namespace: controllermeta.InferenceNamespace, Labels: map[string]string{"sovereign-ai.io/project": "project"}},
		Spec:       v1alpha1.InferenceEndpointSpec{Model: "code", ModelRevision: "v1", Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject, MaxKVRAMMiB: 4000, SafetyHeadroomMiB: 1000},
		Status:     v1alpha1.InferenceEndpointStatus{Phase: v1alpha1.PhaseRunning, ActiveLeases: []v1alpha1.NamespacedReference{ghost}, ActiveLeaseCount: 1, AllocatedKVRAMMiB: 2000},
	}
	lease := &v1alpha1.InferenceLease{
		ObjectMeta: metav1.ObjectMeta{Name: "lease", Namespace: "workflow", UID: "lease-uid"},
		Spec:       v1alpha1.InferenceLeaseSpec{WorkflowRef: v1alpha1.UIDReference{Name: "wf"}, AttemptRef: "architect-001", ProjectRef: "project", Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject, Model: "code", ModelRevision: "v1", EstimatedKVRAMMiB: 2000},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: controllermeta.InferenceNamespace}}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.InferenceLease{}, &v1alpha1.InferenceEndpoint{}).
		WithObjects(namespace, endpoint, lease).Build()
	reconciler := &InferenceLeaseReconciler{Client: client, Scheme: scheme, Audit: audit.NewMemoryRecorder()}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: lease.Namespace, Name: lease.Name}}
	for range 4 {
		if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	var updatedLease v1alpha1.InferenceLease
	if err := client.Get(context.Background(), request.NamespacedName, &updatedLease); err != nil {
		t.Fatal(err)
	}
	if updatedLease.Status.Phase != v1alpha1.PhaseRunning {
		t.Fatalf("lease phase = %q, want Running", updatedLease.Status.Phase)
	}
	var updatedEndpoint v1alpha1.InferenceEndpoint
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: endpoint.Namespace, Name: endpoint.Name}, &updatedEndpoint); err != nil {
		t.Fatal(err)
	}
	if updatedEndpoint.Status.AllocatedKVRAMMiB != lease.Spec.EstimatedKVRAMMiB ||
		updatedEndpoint.Status.ActiveLeaseCount != 1 ||
		containsLeaseReference(updatedEndpoint.Status.ActiveLeases, ghost.Namespace, ghost.Name) ||
		!containsLeaseReference(updatedEndpoint.Status.ActiveLeases, lease.Namespace, lease.Name) {
		t.Fatalf("stale reservation was not repaired: %#v", updatedEndpoint.Status)
	}
}

func TestInferenceLeaseDeletionReleasesEndpointReservation(t *testing.T) {
	scheme := inferenceScheme(t)
	now := metav1.Now()
	leaseRef := v1alpha1.NamespacedReference{Namespace: "workflow", Name: "lease"}
	endpointRef := v1alpha1.NamespacedReference{Namespace: controllermeta.InferenceNamespace, Name: "warm"}
	endpoint := &v1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: endpointRef.Name, Namespace: endpointRef.Namespace},
		Status:     v1alpha1.InferenceEndpointStatus{ActiveLeases: []v1alpha1.NamespacedReference{leaseRef}, ActiveLeaseCount: 1, AllocatedKVRAMMiB: 2000},
	}
	lease := &v1alpha1.InferenceLease{
		ObjectMeta: metav1.ObjectMeta{Name: leaseRef.Name, Namespace: leaseRef.Namespace, UID: "lease-uid", Finalizers: []string{InferenceLeaseFinalizer}, DeletionTimestamp: &now},
		Spec:       v1alpha1.InferenceLeaseSpec{EstimatedKVRAMMiB: 2000},
		Status:     v1alpha1.InferenceLeaseStatus{Phase: v1alpha1.PhaseRunning, EndpointRef: endpointRef},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.InferenceLease{}, &v1alpha1.InferenceEndpoint{}).
		WithObjects(endpoint, lease).Build()
	reconciler := &InferenceLeaseReconciler{Client: client, Scheme: scheme}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: lease.Namespace, Name: lease.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var updatedEndpoint v1alpha1.InferenceEndpoint
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: endpoint.Namespace, Name: endpoint.Name}, &updatedEndpoint); err != nil {
		t.Fatal(err)
	}
	if updatedEndpoint.Status.AllocatedKVRAMMiB != 0 || updatedEndpoint.Status.ActiveLeaseCount != 0 || len(updatedEndpoint.Status.ActiveLeases) != 0 {
		t.Fatalf("endpoint reservation survived lease deletion: %#v", updatedEndpoint.Status)
	}
	var updatedLease v1alpha1.InferenceLease
	err := client.Get(context.Background(), request.NamespacedName, &updatedLease)
	if err == nil && containsString(updatedLease.Finalizers, InferenceLeaseFinalizer) {
		t.Fatalf("inference lease finalizer was not removed: %v", updatedLease.Finalizers)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestInferenceWorkloadUsesKubernetesGPUPlacement(t *testing.T) {
	reconciler := InferenceEndpointReconciler{
		Profile: testInferenceProfile(),
	}
	endpoint := &v1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "endpoint", Namespace: controllermeta.InferenceNamespace},
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
	arguments := map[string]string{}
	for index, argument := range pod.Spec.Containers[0].Args {
		if index+1 < len(pod.Spec.Containers[0].Args) && len(argument) > 2 && argument[:2] == "--" {
			arguments[argument] = pod.Spec.Containers[0].Args[index+1]
		}
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
	if !autoToolChoice || toolCallParser != "qwen3_coder" {
		t.Fatalf("tool calling args = %v, want automatic Qwen3.5 parsing", pod.Spec.Containers[0].Args)
	}
	if arguments["--model"] != reconciler.Profile.ModelID ||
		arguments["--revision"] != reconciler.Profile.ModelRevision ||
		arguments["--served-model-name"] != reconciler.Profile.ServedModelName ||
		arguments["--dtype"] != "bfloat16" || arguments["--quantization"] != "compressed-tensors" ||
		arguments["--attention-backend"] != "" ||
		arguments["--max-model-len"] != "16384" ||
		arguments["--kv-cache-memory-bytes"] != "3221225472" {
		t.Fatalf("9B runtime args = %v", pod.Spec.Containers[0].Args)
	}
	if arguments["--reasoning-parser"] != "qwen3" ||
		arguments["--default-chat-template-kwargs"] != `{"enable_thinking":false}` ||
		arguments["--max-num-batched-tokens"] != "2048" ||
		arguments["--max-num-seqs"] != "1" {
		t.Fatalf("reasoning/memory arguments = %v", pod.Spec.Containers[0].Args)
	}
	if got := pod.Spec.Containers[0].Resources.Limits["nvidia.com/gpu"]; got.String() != "1" {
		t.Fatalf("GPU request = %s, want 1", got.String())
	}
	if service.Spec.Selector["sovereign-ai.io/endpoint-id"] != endpoint.Name || pod.Labels["sovereign-ai.io/endpoint-id"] != endpoint.Name {
		t.Fatal("service selector does not match endpoint pod")
	}
}

func TestInferenceEndpointRequiresConfiguredModelProfile(t *testing.T) {
	reconciler := InferenceEndpointReconciler{Profile: testInferenceProfile()}
	endpoint := &v1alpha1.InferenceEndpoint{Spec: v1alpha1.InferenceEndpointSpec{
		Model:         reconciler.Profile.ServedModelName,
		ModelRevision: reconciler.Profile.ModelRevision,
		RuntimeImage:  reconciler.Profile.RuntimeImage,
	}}
	if err := reconciler.validateEndpointProfile(endpoint); err != nil {
		t.Fatalf("matching endpoint was rejected: %v", err)
	}
	endpoint.Spec.ModelRevision = "488639f1ff808d1d3d0ba301aef8c11461451ec5"
	if err := reconciler.validateEndpointProfile(endpoint); err == nil {
		t.Fatal("endpoint for a stale model revision was accepted")
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
		RuntimeImage:       "docker.io/vllm/vllm-openai@sha256:770fe65b2c73ee74a5c42165cf3433de4048cc2cd9c57a937ca4e35aba5aa87b",
		ModelID:            "cyankiwi/Qwen3.5-9B-AWQ-4bit",
		ModelRevision:      "156edc4bbeb8d1910ee7be9196bafaf1bc052156",
		ServedModelName:    "code-qwen35-9b-awq",
		DType:              "bfloat16",
		Quantization:       "compressed-tensors",
		ToolCallParser:     "qwen3_coder",
		ReasoningParser:    "qwen3",
		LanguageModelOnly:  true,
		MaxModelLen:        16384,
		KVCacheMemoryBytes: 3221225472,
		CachePVCName:       "sovereign-model-cache",
		CachePath:          "/model-cache",
		GPUNodeLabelKey:    "sovereign-ai.io/gpu-node",
		GPUNodeLabelValue:  "true",
		StartupTimeout:     time.Minute * 15,
		RequestTimeout:     time.Minute * 3,
		MaxOutputTokens:    2048,
		MaxResponseBytes:   4194304, // 4 MB
	}
}

func TestInferenceWorkloadOptionalModelFeatures(t *testing.T) {
	profile := testInferenceProfile()
	profile.EnableThinking = true
	r := InferenceEndpointReconciler{Profile: profile}
	ep := &v1alpha1.InferenceEndpoint{}
	pod, _ := r.buildInferenceWorkloads(ep)
	if !containsString(pod.Spec.Containers[0].Args, `{"enable_thinking":true}`) {
		t.Fatal("explicit thinking opt-in was not forwarded")
	}
	r.Profile.ToolCallParser = ""
	r.Profile.ReasoningParser = ""
	r.Profile.LanguageModelOnly = false
	pod, _ = r.buildInferenceWorkloads(ep)
	args := pod.Spec.Containers[0].Args
	if !containsString(args, "hermes") {
		t.Fatal("legacy profiles must retain Hermes parsing")
	}
	for _, absent := range []string{"--reasoning-parser", "--default-chat-template-kwargs", "--language-model-only"} {
		if containsString(args, absent) {
			t.Fatalf("disabled model feature still present: %s", absent)
		}
	}
}
