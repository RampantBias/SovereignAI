package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/controllers"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWorkflowBootstrapGatesFirstAttemptUntilArtifactAcceptance(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for name, add := range map[string]func(*runtime.Scheme) error{
		"sovereign":    v1alpha1.AddToScheme,
		"core":         corev1.AddToScheme,
		"coordination": coordinationv1.AddToScheme,
		"batch":        batchv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("add %s scheme: %v", name, err)
		}
	}

	changeRequestBytes, err := json.Marshal(artifactcontract.ChangeRequest{
		Summary: "Add divide support", Description: "Implement calculator division.",
		AcceptanceCriteria: []string{"84 / 2 returns 42"},
		RepositoryURL:      "https://git.example.test/calculator.git",
		SourceCommit:       strings.Repeat("a", 40),
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := artifactcontract.DigestBytes(changeRequestBytes)
	immutable := true
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wf-bootstrap", Namespace: "wf-bootstrap", UID: "workflow-uid",
			Finalizers: []string{controllermeta.WorkflowFinalizer},
		},
		Spec: v1alpha1.SovereignWorkflowSpec{
			Project:    v1alpha1.UIDReference{Name: "platform", UID: "project-uid"},
			WorkflowID: "wf-bootstrap",
			Bootstrap: v1alpha1.WorkflowBootstrapSpec{
				SourceRef:      v1alpha1.UIDReference{Name: "wf-bootstrap-input", UID: "source-uid"},
				Key:            "change-request.json",
				ExpectedDigest: digest,
				Contract:       v1alpha1.ContractReference{Name: "change-request", Version: "v1"},
				ArtifactName:   "change-request",
			},
			RequestedVolumeSize: "1Gi",
			Steps: []v1alpha1.StepConfig{{
				Name: "architect", Kind: v1alpha1.ExecutionKindAgent,
				Agent: &v1alpha1.AgentStepSpec{Responsibility: "plan", Image: "agent", Executable: []string{"/agent"}},
			}},
		},
		Status: v1alpha1.SovereignWorkflowStatus{Phase: string(v1alpha1.PhasePending)},
	}
	source := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: workflow.Spec.Bootstrap.SourceRef.Name, Namespace: workflow.Namespace,
			UID: workflow.Spec.Bootstrap.SourceRef.UID,
		},
		Immutable:  &immutable,
		BinaryData: map[string][]byte{workflow.Spec.Bootstrap.Key: changeRequestBytes},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: workflow.Namespace}}
	kubeClient := &controllerUIDClient{Client: fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(
			&v1alpha1.SovereignWorkflow{}, &v1alpha1.StepAttempt{}, &v1alpha1.AgentRun{},
			&v1alpha1.Artifact{}, &batchv1.Job{},
		).
		WithObjects(namespace, workflow, source).
		Build()}
	recorder := audit.NewMemoryRecorder()
	workflowReconciler := &WorkflowReconciler{
		Client: kubeClient, Scheme: scheme, Audit: recorder,
		BootstrapImage: "bootstrap:test",
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: workflow.Namespace, Name: workflow.Name,
	}}

	var job batchv1.Job
	for reconcile := 0; reconcile < 8; reconcile++ {
		if _, err := workflowReconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("bootstrap reconcile %d: %v", reconcile, err)
		}
		assertNoStepAttempts(t, ctx, kubeClient)
		err := kubeClient.Get(ctx, types.NamespacedName{
			Namespace: workflow.Namespace, Name: controllers.BootstrapJobName(workflow.Name),
		}, &job)
		if err == nil {
			break
		}
		if !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
	}
	if job.Name == "" {
		t.Fatal("bootstrap Job was not created")
	}
	if job.Spec.Template.Spec.AutomountServiceAccountToken == nil ||
		*job.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("bootstrap Job must not mount a service account token")
	}
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != "bootstrap:test" || !containsString(container.Args, digest) {
		t.Fatalf("unexpected bootstrap container: %#v", container)
	}

	job.Status.Succeeded = 1
	if err := kubeClient.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := workflowReconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	assertNoStepAttempts(t, ctx, kubeClient)

	var artifact v1alpha1.Artifact
	artifactKey := types.NamespacedName{Namespace: workflow.Namespace, Name: workflow.Spec.Bootstrap.ArtifactName}
	if err := kubeClient.Get(ctx, artifactKey, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.Spec.Digest != digest ||
		artifact.Spec.Path != controllers.BootstrapArtifactPath(digest) ||
		artifact.Spec.ProducerRef.Kind != "SovereignWorkflow" {
		t.Fatalf("unexpected bootstrap Artifact: %#v", artifact.Spec)
	}

	artifactReconciler := &controllers.ArtifactReconciler{Client: kubeClient, Audit: recorder}
	if _, err := artifactReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: artifactKey}); err != nil {
		t.Fatal(err)
	}
	if err := kubeClient.Get(ctx, artifactKey, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.Status.Phase != v1alpha1.PhaseSucceeded {
		t.Fatalf("bootstrap Artifact phase = %s, want Succeeded", artifact.Status.Phase)
	}

	if _, err := workflowReconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	var attempts v1alpha1.StepAttemptList
	if err := kubeClient.List(ctx, &attempts); err != nil {
		t.Fatal(err)
	}
	if len(attempts.Items) != 1 || attempts.Items[0].Spec.RetryNumber != 1 {
		t.Fatalf("first StepAttempt was not created after acceptance: %#v", attempts.Items)
	}
	var updated v1alpha1.SovereignWorkflow
	if err := kubeClient.Get(ctx, request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}
	if !apiMeta.IsStatusConditionTrue(updated.Status.Conditions, "BootstrapReady") ||
		updated.Status.BootstrapArtifactRef == nil ||
		updated.Status.BootstrapArtifactRef.Name != artifact.Name ||
		updated.Status.BootstrapArtifactRef.UID != artifact.UID {
		t.Fatalf("workflow bootstrap status is incomplete: %#v", updated.Status)
	}
	if !recorder.Has("WorkflowAdmitted") {
		t.Fatalf("WorkflowAdmitted was not recorded after Artifact acceptance: %#v", recorder.AllEvents())
	}

	var deletedSource corev1.ConfigMap
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(source), &deletedSource); !apierrors.IsNotFound(err) {
		t.Fatalf("bootstrap ConfigMap was not cleaned up: %v", err)
	}
	var deletedJob batchv1.Job
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(&job), &deletedJob); !apierrors.IsNotFound(err) {
		t.Fatalf("bootstrap Job was not cleaned up: %v", err)
	}
	var lease coordinationv1.Lease
	if err := kubeClient.Get(ctx, types.NamespacedName{
		Namespace: workflow.Namespace, Name: updated.Status.WorkspaceWriterLeaseRef,
	}, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "" {
		t.Fatalf("bootstrap writer lease was not released: %#v", lease.Spec)
	}
}

type controllerUIDClient struct {
	client.Client
}

func (c *controllerUIDClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if object.GetUID() == "" {
		object.SetUID(types.UID("test-" + object.GetName()))
	}
	return c.Client.Create(ctx, object, options...)
}

func assertNoStepAttempts(t *testing.T, ctx context.Context, kubeClient client.Client) {
	t.Helper()
	var attempts v1alpha1.StepAttemptList
	if err := kubeClient.List(ctx, &attempts); err != nil {
		t.Fatal(err)
	}
	if len(attempts.Items) != 0 {
		t.Fatalf("StepAttempt was created before bootstrap acceptance: %#v", attempts.Items)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
