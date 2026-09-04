package utilityoperation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/controllers"
	policyengine "github.com/SovereignAI/internal/policy"
	"github.com/SovereignAI/internal/utilitycontract"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUtilityOperationCannotActWithoutStepAttemptAuthority(t *testing.T) {
	scheme := attemptScheme(t)
	workflow := workflowFixture()
	attempt := authorizedAttempt("push-001", "wf", "push", v1alpha1.ExecutionKindUtility)
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: attempt.Name, Namespace: attempt.Namespace, UID: "unowned-operation"},
		Spec: v1alpha1.UtilityOperationSpec{AttemptRef: attempt.Name, WorkflowRef: workflowRef(workflow), StepName: "push", Attempt: 1,
			Operation: v1alpha1.UtilityOperationRequest{Name: "git.push", Parameters: map[string]string{"branch": "main"}}},
		Status: v1alpha1.UtilityOperationStatus{Phase: v1alpha1.PhasePending},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.UtilityOperation{}).WithObjects(attempt, operation).Build()
	reconciler := &UtilityOperationReconciler{Client: client, Scheme: scheme, Policy: allowPolicy{}}
	if _, err := reconciler.Reconcile(context.Background(), requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	var updated v1alpha1.UtilityOperation
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != v1alpha1.PhaseFailed || updated.Status.FailureReason != "InvalidStepAttemptAuthority" {
		t.Fatalf("unowned utility operation was not rejected: %#v", updated.Status)
	}
	var jobs batchv1.JobList
	if err := client.List(context.Background(), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("unowned utility operation created jobs: %#v", jobs.Items)
	}
}

func TestUtilityCollectionPreservesDeclaredJobFailure(t *testing.T) {
	scheme := attemptScheme(t)
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "test-candidate-001", Namespace: "wf"},
		Status: v1alpha1.UtilityOperationStatus{
			Phase: v1alpha1.PhaseCollecting, CollectorJobRef: "test-candidate-001-collect",
			FailureReason: "UtilityJobFailed", Retryable: true,
		},
	}
	collector := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: operation.Status.CollectorJobRef, Namespace: operation.Namespace},
		Status:     batchv1.JobStatus{Succeeded: 1},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.UtilityOperation{}).
		WithObjects(operation, collector).Build()
	reconciler := &UtilityOperationReconciler{Client: client, Scheme: scheme}
	if _, err := reconciler.reconcileCollection(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	var updated v1alpha1.UtilityOperation
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != v1alpha1.PhaseFailed || updated.Status.FailureReason != "UtilityJobFailed" || !updated.Status.Retryable {
		t.Fatalf("collected failed result advanced as success: %#v", updated.Status)
	}
}

func TestUtilityOperationCreatesProjectConstrainedJobIdempotently(t *testing.T) {
	scheme := attemptScheme(t)
	workflow := workflowFixture()
	project := projectFixture()
	attempt := authorizedAttempt("tests-001", "wf", "tests", v1alpha1.ExecutionKindUtility)
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "tests-001", Namespace: "wf", UID: "tests-operation-uid", Labels: map[string]string{controllermeta.LabelWorkflow: "wf"}},
		Spec: v1alpha1.UtilityOperationSpec{
			AttemptRef: "tests-001", WorkflowRef: workflowRef(workflow), StepName: "tests", Attempt: 1,
			Operation:       v1alpha1.UtilityOperationRequest{Name: "test.run"},
			OutputContracts: []v1alpha1.ContractReference{{Name: "test-report", Version: "v1"}},
		},
		Status: v1alpha1.UtilityOperationStatus{Phase: v1alpha1.PhasePending},
	}
	ownByAttempt(operation, attempt)
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.UtilityOperation{}).
		WithObjects(workflow, workspaceLeaseFixture(), project, attempt, operation).Build()
	reconciler := &UtilityOperationReconciler{Client: client, Scheme: scheme, Policy: allowPolicy{}}
	for range 2 {
		if _, err := reconciler.Reconcile(context.Background(), requestFor(operation)); err != nil {
			t.Fatal(err)
		}
	}
	var jobs batchv1.JobList
	if err := client.List(context.Background(), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("utility reconciliation created %d jobs, want one", len(jobs.Items))
	}
	job := jobs.Items[0]
	if job.Spec.Template.Spec.Containers[0].Image != project.Spec.TestJob.Image {
		t.Fatalf("utility job image = %q, want Project test image", job.Spec.Template.Spec.Containers[0].Image)
	}
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatal("Project image must receive the trusted utility runtime")
	}
	var input corev1.ConfigMap
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name + "-input"}, &input); err != nil {
		t.Fatal(err)
	}
	var contract utilitycontract.Input
	if err := json.Unmarshal([]byte(input.Data["input.json"]), &contract); err != nil {
		t.Fatal(err)
	}
	if len(contract.Command) != 2 || contract.Command[0] != "go" || contract.Operation != "test.run" {
		t.Fatalf("unexpected utility contract: %#v", contract)
	}
	if contract.Parameters["environmentImageDigest"] != artifactcontract.DigestBytes([]byte(project.Spec.TestJob.Image)) {
		t.Fatalf("test environment identity was not derived from Project state: %#v", contract.Parameters)
	}
	if contract.Authority.Kind != "UtilityOperation" || contract.Authority.Name != operation.Name || contract.PolicyDecisionID != "allow-test" {
		t.Fatalf("utility contract lost its authority lineage: %#v", contract)
	}
	if contract.WorkspaceWrite.WriterEpoch != 1 || contract.WorkspaceWrite.LeaseName != workflow.Status.WorkspaceWriterLeaseRef {
		t.Fatalf("utility contract lost workspace writer authority: %#v", contract.WorkspaceWrite)
	}
}

func TestPrivilegedUtilityOperationFailsClosedWithoutPolicy(t *testing.T) {
	scheme := attemptScheme(t)
	attempt := authorizedAttempt("commit-001", "wf", "commit", v1alpha1.ExecutionKindUtility)
	workflow := workflowFixture()
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "commit-001", Namespace: "wf"},
		Spec: v1alpha1.UtilityOperationSpec{AttemptRef: "commit-001", WorkflowRef: workflowRef(workflow), StepName: "commit", Attempt: 1,
			Operation: v1alpha1.UtilityOperationRequest{Name: "git.commit", Parameters: map[string]string{"message": "change"}}},
		Status: v1alpha1.UtilityOperationStatus{Phase: v1alpha1.PhasePending},
	}
	ownByAttempt(operation, attempt)
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.UtilityOperation{}).WithObjects(attempt, operation).Build()
	reconciler := &UtilityOperationReconciler{Client: client, Scheme: scheme}
	if _, err := reconciler.Reconcile(context.Background(), requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	var updated v1alpha1.UtilityOperation
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != v1alpha1.PhaseFailed || updated.Status.FailureReason != "UtilityOperationDenied" {
		t.Fatalf("privileged operation did not fail closed: %#v", updated.Status)
	}
}

func TestUtilityOperationScopesRegistryCredentialToUtilityContainer(t *testing.T) {
	scheme := attemptScheme(t)
	workflow := workflowFixture()
	project := projectFixture()
	project.Spec.Validation.ImageName = "registry.example.test/app"
	project.Spec.BuildJob.CredentialRef = v1alpha1.NamespacedReference{Namespace: "credentials", Name: "registry"}
	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "credentials", UID: "registry-uid"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.example.test":{}}}`)},
	}
	attempt := authorizedAttempt("build-001", "wf", "build", v1alpha1.ExecutionKindUtility)
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "build-001", Namespace: "wf", UID: "operation-uid"},
		Spec: v1alpha1.UtilityOperationSpec{AttemptRef: "build-001", WorkflowRef: workflowRef(workflow), StepName: "build", Attempt: 1,
			Operation: v1alpha1.UtilityOperationRequest{Name: "build.image"}},
		Status: v1alpha1.UtilityOperationStatus{Phase: v1alpha1.PhasePending},
	}
	ownByAttempt(operation, attempt)
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.UtilityOperation{}).
		WithObjects(workflow, workspaceLeaseFixture(), project, source, attempt, operation).Build()
	reconciler := &UtilityOperationReconciler{Client: client, Scheme: scheme, Policy: allowPolicy{}}
	if _, err := reconciler.Reconcile(context.Background(), requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	var credential corev1.Secret
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name + "-credential"}, &credential); err != nil {
		t.Fatal(err)
	}
	if credential.Immutable == nil || !*credential.Immutable {
		t.Fatal("operation-scoped credential must be immutable")
	}
	if len(credential.OwnerReferences) != 1 || credential.OwnerReferences[0].Kind != "UtilityOperation" {
		t.Fatalf("credential authority owner = %#v", credential.OwnerReferences)
	}
	var job batchv1.Job
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name}, &job); err != nil {
		t.Fatal(err)
	}
	if len(job.Spec.Template.Spec.InitContainers) != 1 || len(job.Spec.Template.Spec.InitContainers[0].VolumeMounts) != 1 {
		t.Fatal("trusted runtime init container unexpectedly received credential material")
	}
	var inputConfig corev1.ConfigMap
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name + "-input"}, &inputConfig); err != nil {
		t.Fatal(err)
	}
	var buildContract utilitycontract.Input
	if err := json.Unmarshal([]byte(inputConfig.Data["input.json"]), &buildContract); err != nil {
		t.Fatal(err)
	}
	if buildContract.Parameters["imageName"] != project.Spec.Validation.ImageName ||
		buildContract.Parameters["builderImageDigest"] != artifactcontract.DigestBytes([]byte(project.Spec.BuildJob.Image)) ||
		buildContract.Parameters["digestFile"] != "image-metadata.json" || buildContract.Parameters["dockerfile"] != "Dockerfile" {
		t.Fatalf("build evidence parameters were not derived from Project state: %#v", buildContract.Parameters)
	}
	main := job.Spec.Template.Spec.Containers[0]
	foundCredential := false
	for _, mount := range main.VolumeMounts {
		if mount.Name == "operation-credential" {
			foundCredential = true
		}
	}
	if !foundCredential {
		t.Fatal("utility container did not receive its operation-scoped credential")
	}
}

func TestUtilityOperationBuildWithoutRegistryCredential(t *testing.T) {
	scheme := attemptScheme(t)
	workflow := workflowFixture()
	project := projectFixture()
	project.Spec.Validation.ImageName = "registry.example.test/app"
	project.Spec.BuildJob.CredentialRef = v1alpha1.NamespacedReference{}
	attempt := authorizedAttempt("build-001", "wf", "build", v1alpha1.ExecutionKindUtility)
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "build-001", Namespace: "wf", UID: "operation-uid"},
		Spec: v1alpha1.UtilityOperationSpec{AttemptRef: "build-001", WorkflowRef: workflowRef(workflow), StepName: "build", Attempt: 1,
			Operation: v1alpha1.UtilityOperationRequest{Name: "build.image"}},
		Status: v1alpha1.UtilityOperationStatus{Phase: v1alpha1.PhasePending},
	}
	ownByAttempt(operation, attempt)
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.UtilityOperation{}).
		WithObjects(workflow, workspaceLeaseFixture(), project, attempt, operation).Build()
	reconciler := &UtilityOperationReconciler{Client: client, Scheme: scheme, Policy: allowPolicy{}}
	if _, err := reconciler.Reconcile(context.Background(), requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	var credential corev1.Secret
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name + "-credential"}, &credential); err == nil {
		t.Fatal("anonymous build unexpectedly received an operation-scoped credential")
	}
	var job batchv1.Job
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name}, &job); err != nil {
		t.Fatal(err)
	}
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.Name == "operation-credential" {
			t.Fatal("anonymous build unexpectedly received a credential volume")
		}
	}
	main := job.Spec.Template.Spec.Containers[0]
	for _, mount := range main.VolumeMounts {
		if mount.Name == "operation-credential" {
			t.Fatal("anonymous build unexpectedly received a credential mount")
		}
	}
	for _, env := range main.Env {
		if env.Name == "DOCKER_CONFIG" {
			t.Fatal("anonymous build unexpectedly received DOCKER_CONFIG")
		}
	}
}

func TestUtilityOperationScopesRepositoryCredentialToHTTPSGit(t *testing.T) {
	scheme := attemptScheme(t)
	workflow := workflowFixture()
	project := projectFixture()
	project.Spec.ApplicationRepository.CredentialRef = v1alpha1.NamespacedReference{Namespace: "credentials", Name: "calculator-git"}
	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "calculator-git", Namespace: "credentials", UID: "calculator-git-uid"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{controllermeta.RepositoryCredentialKey: []byte("https://demo-user:encoded-token@example.test\n")},
	}
	attempt := authorizedAttempt("initialize-001", "wf", "initialize", v1alpha1.ExecutionKindUtility)
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "initialize-001", Namespace: "wf", UID: "initialize-operation-uid"},
		Spec: v1alpha1.UtilityOperationSpec{
			AttemptRef: "initialize-001", WorkflowRef: workflowRef(workflow), StepName: "initialize", Attempt: 1,
			Operation: v1alpha1.UtilityOperationRequest{Name: "repository.initialize"},
		},
		Status: v1alpha1.UtilityOperationStatus{Phase: v1alpha1.PhasePending},
	}
	ownByAttempt(operation, attempt)
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.UtilityOperation{}).
		WithObjects(workflow, workspaceLeaseFixture(), project, sourceSecret, attempt, operation).Build()
	reconciler := &UtilityOperationReconciler{Client: client, Scheme: scheme, Policy: allowPolicy{}}
	if _, err := reconciler.Reconcile(context.Background(), requestFor(operation)); err != nil {
		t.Fatal(err)
	}

	var credential corev1.Secret
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name + "-credential"}, &credential); err != nil {
		t.Fatal(err)
	}
	if len(credential.Data) != 1 || string(credential.Data[controllermeta.RepositoryCredentialKey]) != string(sourceSecret.Data[controllermeta.RepositoryCredentialKey]) {
		t.Fatalf("operation credential did not preserve the exact repository credential contract: %#v", credential.Data)
	}

	var job batchv1.Job
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: operation.Namespace, Name: operation.Name}, &job); err != nil {
		t.Fatal(err)
	}
	var credentialSource *corev1.SecretVolumeSource
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.Name == "operation-credential" {
			credentialSource = volume.Secret
			break
		}
	}
	if credentialSource == nil || len(credentialSource.Items) != 1 ||
		credentialSource.Items[0].Key != controllermeta.RepositoryCredentialKey ||
		credentialSource.Items[0].Path != controllermeta.RepositoryCredentialKey {
		t.Fatalf("repository credential volume exposes unexpected keys: %#v", credentialSource)
	}

	environment := make(map[string]string)
	for _, variable := range job.Spec.Template.Spec.Containers[0].Env {
		environment[variable.Name] = variable.Value
	}
	if environment["GIT_CONFIG_COUNT"] != "2" ||
		environment["GIT_CONFIG_VALUE_0"] != "" ||
		environment["GIT_CONFIG_VALUE_1"] != "store --file=/var/run/sovereign/credentials/credentials" {
		t.Fatalf("Git credential helper configuration = %#v", environment)
	}
	if environment["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q, want 0", environment["GIT_TERMINAL_PROMPT"])
	}
	if _, found := environment["GIT_SSH_COMMAND"]; found {
		t.Fatal("HTTPS-only repository operation received SSH configuration")
	}
}

func TestRepositoryCredentialSecretContract(t *testing.T) {
	const validEntry = "https://demo-user:token%3Awith%40symbols@example.test\n"

	tests := []struct {
		name    string
		secret  *corev1.Secret
		wantErr bool
	}{
		{
			name:   "one HTTPS credential-store entry",
			secret: &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{controllermeta.RepositoryCredentialKey: []byte(validEntry)}},
		},
		{
			name:    "wrong Secret type",
			secret:  &corev1.Secret{Type: corev1.SecretTypeBasicAuth, Data: map[string][]byte{controllermeta.RepositoryCredentialKey: []byte(validEntry)}},
			wantErr: true,
		},
		{
			name:    "missing credentials key",
			secret:  &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"username": []byte("demo-user")}},
			wantErr: true,
		},
		{
			name: "extra key",
			secret: &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{
				controllermeta.RepositoryCredentialKey: []byte(validEntry),
				"extra":                                []byte("not-allowed"),
			}},
			wantErr: true,
		},
		{
			name:    "empty entry",
			secret:  &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{controllermeta.RepositoryCredentialKey: nil}},
			wantErr: true,
		},
		{
			name: "multiple entries",
			secret: &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{
				controllermeta.RepositoryCredentialKey: []byte(validEntry + "https://other-user:other-secret@example.test\n"),
			}},
			wantErr: true,
		},
		{
			name:    "HTTP entry",
			secret:  &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{controllermeta.RepositoryCredentialKey: []byte("http://demo-user:secret@example.test\n")}},
			wantErr: true,
		},
		{
			name:    "missing username",
			secret:  &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{controllermeta.RepositoryCredentialKey: []byte("https://:secret@example.test\n")}},
			wantErr: true,
		},
		{
			name:    "missing secret",
			secret:  &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{controllermeta.RepositoryCredentialKey: []byte("https://demo-user@example.test\n")}},
			wantErr: true,
		},
		{
			name:    "invalid URL encoding",
			secret:  &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{controllermeta.RepositoryCredentialKey: []byte("https://demo-user:secret%ZZ@example.test\n")}},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := repositoryCredentialData(test.secret)
			if test.wantErr {
				if err == nil {
					t.Fatal("repositoryCredentialData() error = nil, want contract rejection")
				}
				if strings.Contains(err.Error(), "token%3Awith%40symbols") || strings.Contains(err.Error(), "other-secret") {
					t.Fatalf("credential validation error exposed secret material: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("repositoryCredentialData() error = %v", err)
			}
			if len(data) != 1 || string(data[controllermeta.RepositoryCredentialKey]) != validEntry {
				t.Fatalf("repository credential data = %#v, want only the original credentials entry", data)
			}
		})
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

func authorizedAttempt(name, namespace, step string, kind v1alpha1.ExecutionKind) *v1alpha1.StepAttempt {
	return &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID("uid-" + name)},
		Spec:       v1alpha1.StepAttemptSpec{WorkflowRef: v1alpha1.UIDReference{Name: "wf", UID: "workflow-uid"}, StepName: step, RetryNumber: 1, Kind: kind, WorkflowAttempt: 1},
		Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhasePending, ExecutionRef: &v1alpha1.TypedLocalReference{
			APIVersion: v1alpha1.GroupVersion.String(), Kind: controllers.DomainKind(kind), Name: name,
		}},
	}
}

func workflowRef(workflow *v1alpha1.SovereignWorkflow) v1alpha1.UIDReference {
	return v1alpha1.UIDReference{
		Name: workflow.Name,
		UID:  types.UID(workflow.UID),
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
func ownByAttempt(object metav1.Object, attempt *v1alpha1.StepAttempt) {
	object.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(attempt, v1alpha1.GroupVersion.WithKind("StepAttempt"))})
}

type allowPolicy struct{}

func (allowPolicy) Evaluate(context.Context, any) (policyengine.Decision, error) {
	return policyengine.Decision{ID: "allow-test", Allowed: true}, nil
}
func TestResolveUtilityInputsUsesPinnedArtifactWithPreservedHistory(t *testing.T) {
	scheme := attemptScheme(t)
	workflowRef := v1alpha1.UIDReference{Name: "wf", UID: "workflow-uid"}
	const generation = int64(1)

	acceptedArtifact := func(name string, uid types.UID, digest, path, producer string) *v1alpha1.Artifact {
		return &v1alpha1.Artifact{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "wf", UID: uid, Generation: generation,
			},
			Spec: v1alpha1.ArtifactSpec{
				WorkflowRef: workflowRef,
				ProducerRef: v1alpha1.TypedLocalReference{
					APIVersion: v1alpha1.GroupVersion.String(),
					Kind:       "AgentRun",
					Name:       producer,
				},
				Contract: v1alpha1.ContractReference{Name: "change-set", Version: "v1"},
				Digest:   digest,
				Path:     path,
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

	oldArtifact := acceptedArtifact(
		"changes-old",
		"changes-old-uid",
		"sha256:old",
		"/workspace/.sovereign/artifacts/old",
		"developer-w000-r002",
	)
	currentArtifact := acceptedArtifact(
		"changes-current",
		"changes-current-uid",
		"sha256:current",
		"/workspace/.sovereign/artifacts/current",
		"developer-w001-r001",
	)
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "prepare-candidate-w001-r001", Namespace: "wf"},
		Spec: v1alpha1.UtilityOperationSpec{
			WorkflowRef: workflowRef,
			Inputs: []v1alpha1.ArtifactReference{{
				Name:   "change-set",
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
	reconciler := &UtilityOperationReconciler{Client: kubeClient, Scheme: scheme}

	inputs, err := reconciler.resolveUtilityInputs(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 ||
		inputs[0].Digest != currentArtifact.Spec.Digest ||
		inputs[0].Path != currentArtifact.Spec.Path {
		t.Fatalf("utility consumed the wrong preserved artifact: %#v", inputs)
	}
}
