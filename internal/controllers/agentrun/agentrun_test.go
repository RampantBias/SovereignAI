package agentrun

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/controllers"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func acceptedAgentInputArtifact(name, contractName, contractVersion, digest, path string, workflowRef v1alpha1.UIDReference) *v1alpha1.Artifact {
	const generation = int64(1)
	return &v1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "wf",
			UID:        types.UID(name + "-uid"),
			Generation: generation,
		},
		Spec: v1alpha1.ArtifactSpec{
			WorkflowRef: workflowRef,
			ProducerRef: v1alpha1.TypedLocalReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       "AgentRun",
				Name:       "producer-001",
			},
			ProducerUID: "producer-001-uid",
			Contract: v1alpha1.ContractReference{
				Name:    contractName,
				Version: contractVersion,
			},
			Digest: digest,
			Path:   path,
		},
		Status: v1alpha1.ArtifactStatus{
			ObservedGeneration: generation,
			Phase:              v1alpha1.PhaseSucceeded,
			Conditions: []metav1.Condition{{
				Type:               "Valid",
				Status:             metav1.ConditionTrue,
				Reason:             "ContractAccepted",
				ObservedGeneration: generation,
			}},
		},
	}
}
func TestAgentRunCreatesRestrictedPod(t *testing.T) {
	scheme := attemptScheme(t)
	workflow := workflowFixture()
	attempt := authorizedAttempt("architect-001", "wf", "architect", v1alpha1.ExecutionKindAgent)
	inputArtifact := acceptedAgentInputArtifact(
		"initialize-repository-a1-repository-revision-v1-00",
		"repository-revision",
		"v1",
		"sha256:repository-revision",
		"/workspace/.sovereign/artifacts/repository-revision.json",
		workflowRef(workflow),
	)
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "architect-001", Namespace: "wf", UID: "architect-run-uid", Labels: map[string]string{controllermeta.LabelWorkflow: "wf"}},
		Spec: v1alpha1.AgentRunSpec{
			AttemptRef:      "architect-001",
			WorkflowRef:     workflowRef(workflow),
			StepName:        "architect",
			Attempt:         1,
			Responsibility:  "plan",
			Capabilities:    []string{agentcontract.CapabilityWorkspaceRead, agentcontract.CapabilityCandidateWrite},
			Image:           "agent@sha256:test",
			Executable:      []string{"/domain-agent", "--role", "architect"},
			PriorAttemptRef: &v1alpha1.FailedAgentAttempt{PreviousAttemptRef: "architect-000", Code: "InvalidRepositoryPath", Message: "affected path was invalid"},
			Inputs:          []v1alpha1.ArtifactReference{{Name: "repository-revision", Digest: "sha256:repository-revision"}},
			OutputContracts: []v1alpha1.ContractReference{{Name: "implementation-plan", Version: "v1"}},
		},
		Status: v1alpha1.AgentRunStatus{Phase: v1alpha1.PhasePending},
	}
	ownByAttempt(run, attempt)
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.AgentRun{}).
		WithObjects(workflow, workspaceLeaseFixture(), attempt, run, inputArtifact, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "api-tls", Namespace: "system"},
			Data:       map[string][]byte{"ca.crt": []byte("public-ca")}},
		).Build()
	recorder := audit.NewMemoryRecorder()
	reconciler := &AgentRunReconciler{Client: client, Scheme: scheme, Audit: recorder, ContextAPIAddress: "api:8080", ContextTLSNamespace: "system", ContextTLSSecret: "api-tls"}
	if _, err := reconciler.Reconcile(context.Background(), requestFor(run)); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, &pod); err != nil {
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
	if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Name != "mcp" {
		t.Fatalf("agent pod MCP sidecar = %#v", pod.Spec.InitContainers)
	}
	sidecar := pod.Spec.InitContainers[0]
	if sidecar.RestartPolicy == nil || *sidecar.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatalf("MCP container is not a native sidecar: %#v", sidecar.RestartPolicy)
	}
	if len(sidecar.VolumeMounts) != 3 || !sidecar.VolumeMounts[0].ReadOnly ||
		sidecar.VolumeMounts[0].MountPath != "/repository" || sidecar.VolumeMounts[1].MountPath != "/workspace" ||
		!sidecar.VolumeMounts[2].ReadOnly || sidecar.VolumeMounts[2].MountPath != "/control" {
		t.Fatalf("unexpected MCP mounts: %#v", sidecar.VolumeMounts)
	}
	foundAgentInput := false
	for _, variable := range sidecar.Env {
		if variable.Name == "SOVEREIGN_AGENT_INPUT" && variable.Value == "/control/input.json" {
			foundAgentInput = true
			break
		}
	}
	if !foundAgentInput {
		t.Fatalf("MCP sidecar is missing SOVEREIGN_AGENT_INPUT: %#v", sidecar.Env)
	}
	if pod.Annotations[controllers.AnnotationWorkspaceWriterEpoch] != "1" {
		t.Fatalf("agent pod writer epoch = %q, want 1", pod.Annotations[controllers.AnnotationWorkspaceWriterEpoch])
	}
	var input corev1.ConfigMap
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: run.Namespace, Name: run.Name + "-input"}, &input); err != nil {
		t.Fatal(err)
	}
	var contract agentcontract.Input
	if err := json.Unmarshal([]byte(input.Data["input.json"]), &contract); err != nil {
		t.Fatal(err)
	}
	if contract.ContextCapture == nil || contract.ContextCapture.AgentRunUID != string(run.UID) {
		t.Fatal("missing runtime snapshot config")
	}
	var upload corev1.Secret
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: run.Namespace, Name: agentcontract.ContextCredentialName(run.Name)}, &upload); err != nil {
		t.Fatal(err)
	}
	if !metav1.IsControlledBy(&upload, run) || len(upload.Data["token"]) != 64 {
		t.Fatal("upload credential is not scoped to run")
	}
	if len(pod.Spec.Containers[0].VolumeMounts) != 3 || pod.Spec.Containers[0].VolumeMounts[2].MountPath != "/context-upload" {
		t.Fatal("missing private upload credential mount")
	}
	before := string(upload.Data["token"])
	if _, err := reconciler.ensureContextCapture(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: run.Namespace, Name: upload.Name}, &upload); err != nil {
		t.Fatal(err)
	}
	if before != string(upload.Data["token"]) {
		t.Fatal("reconciliation rotated upload token")
	}
	if contract.Responsibility != "plan" || len(contract.Outputs) != 1 || len(contract.Inputs) != 1 {
		t.Fatalf("unexpected agent contract: %#v", contract)
	}
	if contract.RetryFeedback == nil || contract.RetryFeedback.PreviousAttemptRef != "architect-000" ||
		contract.RetryFeedback.Code != "InvalidRepositoryPath" || contract.RetryFeedback.Message != "affected path was invalid" {
		t.Fatalf("agent contract lost retry feedback: %#v", contract.RetryFeedback)
	}
	if !contract.Outputs[0].Required || contract.Outputs[0].MediaType != "application/json" {
		t.Fatalf("agent output obligation is not required JSON: %#v", contract.Outputs[0])
	}
	if contract.Inputs[0].Name != "repository-revision" ||
		contract.Inputs[0].Contract != "repository-revision/v1" ||
		contract.Inputs[0].Digest != inputArtifact.Spec.Digest ||
		contract.Inputs[0].Path != inputArtifact.Spec.Path {
		t.Fatalf("agent contract did not resolve the logical artifact input: %#v", contract.Inputs[0])
	}
	if contract.WorkspaceWrite.WriterEpoch != 1 || contract.WorkspaceWrite.LeaseName != workflow.Status.WorkspaceWriterLeaseRef {
		t.Fatalf("agent contract lost workspace writer authority: %#v", contract.WorkspaceWrite)
	}
	if contract.MCPServer != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("agent contract MCP endpoint = %q", contract.MCPServer)
	}
	var inputsResolved, established, authorized audit.Event
	for _, event := range recorder.AllEvents() {
		switch event.Type {
		case "InputsResolved":
			inputsResolved = event
		case "ExecutionAuthorityEstablished":
			established = event
		case "AgentExecutionAuthorized":
			authorized = event
		}
	}
	if inputsResolved.ID == "" {
		t.Fatal("InputsResolved event was not recorded before pod creation")
	}
	if established.ID == "" {
		t.Fatal("ExecutionAuthorityEstablished event was not recorded before pod creation")
	}
	if authorized.ID == "" {
		t.Fatal("AgentExecutionAuthorized event was not recorded before pod creation")
	}
	var inputs audit.InputsResolved
	if err := json.Unmarshal(inputsResolved.Data, &inputs); err != nil {
		t.Fatalf("decode InputsResolved payload: %v", err)
	}
	if inputs.SchemaVersion != audit.PayloadSchemaVersionV1 || inputs.Consumer.Kind != "AgentRun" ||
		inputs.Consumer.UID != string(run.UID) || len(inputs.Inputs) != 1 ||
		inputs.Inputs[0].Artifact.UID != string(inputArtifact.UID) ||
		inputs.Inputs[0].Contract != "repository-revision/v1" ||
		inputs.Inputs[0].Digest != inputArtifact.Spec.Digest ||
		inputs.Inputs[0].Producer.UID != string(inputArtifact.Spec.ProducerUID) {
		t.Fatalf("unexpected InputsResolved payload: %#v", inputs)
	}
	var authority audit.AuthorityEstablished
	if err := json.Unmarshal(established.Data, &authority); err != nil {
		t.Fatalf("decode ExecutionAuthorityEstablished payload: %v", err)
	}
	if authority.SchemaVersion != audit.PayloadSchemaVersionV1 ||
		authority.Workflow.UID != string(workflow.UID) || authority.StepAttempt.UID != string(attempt.UID) ||
		authority.Primitive.UID != string(run.UID) || authority.WorkspaceWrite == nil ||
		authority.WorkspaceWrite.Lease.UID != "workspace-writer-uid" ||
		authority.WorkspaceWrite.HolderIdentity == "" || authority.WorkspaceWrite.WriterEpoch != 1 ||
		len(authority.WorkspaceWrite.Capabilities) != 2 || len(authority.Invariants) != 3 {
		t.Fatalf("unexpected ExecutionAuthorityEstablished payload: %#v", authority)
	}
	for _, invariant := range authority.Invariants {
		if invariant.Outcome != "passed" || invariant.Expected != invariant.Observed {
			t.Fatalf("ExecutionAuthorityEstablished invariant is not verified: %#v", invariant)
		}
	}
	var decision audit.DecisionEvaluated
	if err := json.Unmarshal(authorized.Data, &decision); err != nil {
		t.Fatalf("decode AgentExecutionAuthorized payload: %v", err)
	}
	if decision.SchemaVersion != audit.PayloadSchemaVersionV1 || decision.Primitive.UID != string(run.UID) ||
		decision.Decision.ID == "" || decision.Decision.Kind != "controller" || decision.Decision.Outcome != "allowed" ||
		decision.Decision.InputDigest == "" || authorized.DecisionID != decision.Decision.ID ||
		decision.AuthorityEvent != established.ID || decision.InputEvent != inputsResolved.ID || len(decision.Invariants) != 3 {
		t.Fatalf("unexpected AgentExecutionAuthorized payload: event=%#v payload=%#v", authorized, decision)
	}
	for _, invariant := range decision.Invariants {
		if invariant.Outcome != "passed" || invariant.Expected != invariant.Observed {
			t.Fatalf("AgentExecutionAuthorized invariant is not verified: %#v", invariant)
		}
	}
}

func TestTerminalAgentRunReleasesInferenceCapacityForNextAttempt(t *testing.T) {
	ctx := context.Background()
	scheme := inferenceScheme(t)
	workflowNamespace := "workflow"
	endpoint := &v1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "warm", Namespace: controllermeta.InferenceNamespace, Labels: map[string]string{"sovereign-ai.io/project": "project"}},
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
	endpointRef := v1alpha1.NamespacedReference{Namespace: endpoint.Namespace, Name: endpoint.Name}
	liveLease := func(name, attemptRef string) *v1alpha1.InferenceLease {
		return &v1alpha1.InferenceLease{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: workflowNamespace},
			Spec: v1alpha1.InferenceLeaseSpec{
				WorkflowRef: v1alpha1.UIDReference{Name: "wf"}, AttemptRef: attemptRef, ProjectRef: "project",
				Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject,
				Model: "code", ModelRevision: "v1", EstimatedKVRAMMiB: 2048,
			},
			Status: v1alpha1.InferenceLeaseStatus{Phase: v1alpha1.PhaseRunning, EndpointRef: endpointRef},
		}
	}
	architectLease := liveLease("architect-001-inference", "architect-001")
	firstTestAuthorLease := liveLease("test-author-001-inference", "test-author-001")
	completedLease := &v1alpha1.InferenceLease{
		ObjectMeta: metav1.ObjectMeta{Name: "test-author-002-inference", Namespace: workflowNamespace},
		Spec: v1alpha1.InferenceLeaseSpec{
			WorkflowRef: v1alpha1.UIDReference{Name: "wf"}, AttemptRef: "test-author-002", ProjectRef: "project",
			Tenant: "team", Classification: "internal", SharingScope: v1alpha1.SharingWithinProject,
			Model: "code", ModelRevision: "v1", EstimatedKVRAMMiB: 2048,
		},
		Status: v1alpha1.InferenceLeaseStatus{
			Phase:       v1alpha1.PhaseRunning,
			EndpointRef: endpointRef,
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
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: controllermeta.InferenceNamespace}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.InferenceLease{}, &v1alpha1.InferenceEndpoint{}).
		WithObjects(namespace, endpoint, architectLease, firstTestAuthorLease, completedLease, nextLease).Build()
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
	if requested.Annotations[controllermeta.InferenceLeaseReleaseRequestAnnotation] != "AgentRunFailed" {
		t.Fatalf("release request annotation = %q", requested.Annotations[controllermeta.InferenceLeaseReleaseRequestAnnotation])
	}

	leaseReconciler := &controllers.InferenceLeaseReconciler{Client: kubeClient, Scheme: scheme, Audit: recorder}
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
		Name: "wf-workspace-writer", Namespace: "wf", UID: "workspace-writer-uid",
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

func authorizedAttempt(name, namespace, step string, kind v1alpha1.ExecutionKind) *v1alpha1.StepAttempt {
	return &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID("uid-" + name)},
		Spec: v1alpha1.StepAttemptSpec{WorkflowRef: v1alpha1.UIDReference{
			Name: "wf",
			UID:  "workflow-uid"},
			StepName:        step,
			RetryNumber:     1,
			Kind:            kind,
			WorkflowAttempt: 1},
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

func containsLeaseReference(references []v1alpha1.NamespacedReference, namespace, name string) bool {
	for _, reference := range references {
		if reference.Namespace == namespace && reference.Name == name {
			return true
		}
	}
	return false
}
func TestResolveAgentInputsUsesPinnedArtifactWithPreservedHistory(t *testing.T) {
	scheme := attemptScheme(t)
	workflowRef := v1alpha1.UIDReference{Name: "wf", UID: "workflow-uid"}

	oldArtifact := acceptedAgentInputArtifact(
		"tests-old",
		"test-change-set",
		"v1",
		"sha256:old",
		"/workspace/.sovereign/artifacts/old",
		workflowRef,
	)
	oldArtifact.UID = "tests-old-uid"
	oldArtifact.Spec.ProducerRef = v1alpha1.TypedLocalReference{
		APIVersion: v1alpha1.GroupVersion.String(),
		Kind:       "AgentRun",
		Name:       "test-author-w000-r002",
	}

	currentArtifact := acceptedAgentInputArtifact(
		"tests-current",
		"test-change-set",
		"v1",
		"sha256:current",
		"/workspace/.sovereign/artifacts/current",
		workflowRef,
	)
	currentArtifact.UID = "tests-current-uid"
	currentArtifact.Spec.ProducerRef = v1alpha1.TypedLocalReference{
		APIVersion: v1alpha1.GroupVersion.String(),
		Kind:       "AgentRun",
		Name:       "test-author-w001-r001",
	}

	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "developer-w001-r001", Namespace: "wf"},
		Spec: v1alpha1.AgentRunSpec{
			WorkflowRef: workflowRef,
			Inputs: []v1alpha1.ArtifactReference{{
				Name:   "test-change-set",
				Digest: currentArtifact.Spec.Digest,
				ArtifactRef: &v1alpha1.UIDReference{
					Name: currentArtifact.Name,
					UID:  currentArtifact.UID,
				},
				ProducerAttemptRef: currentArtifact.Spec.ProducerRef.Name,
			}},
		},
	}

	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(oldArtifact, currentArtifact).
		Build()
	reconciler := &AgentRunReconciler{Client: kubeClient, Scheme: scheme}

	inputs, ready, invalidReason, err := reconciler.resolveAgentInputs(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if !ready || invalidReason != "" {
		t.Fatalf("pinned input was not ready: ready=%v reason=%q", ready, invalidReason)
	}
	if len(inputs) != 1 ||
		inputs[0].Digest != currentArtifact.Spec.Digest ||
		inputs[0].Path != currentArtifact.Spec.Path {
		t.Fatalf("agent consumed the wrong preserved artifact: %#v", inputs)
	}
}
