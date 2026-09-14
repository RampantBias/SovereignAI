package workflow

import (
	"context"
	"fmt"
	"testing"
	"time"

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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type bootstrapArtifactFailureClient struct {
	client.Client
	failures int
}

func (c *bootstrapArtifactFailureClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if _, ok := object.(*v1alpha1.Artifact); ok && c.failures > 0 {
		c.failures--
		return fmt.Errorf("injected temporary artifact create failure")
	}
	return c.Client.Create(ctx, object, options...)
}

func bootstrapWriterFixture(t *testing.T, status batchv1.JobStatus) (*WorkflowReconciler, *bootstrapArtifactFailureClient, ctrl.Request, controllers.WorkspaceWriterGrant) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1alpha1.AddToScheme, corev1.AddToScheme, batchv1.AddToScheme, coordinationv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	content, err := artifactcontract.PrepareChangeRequest([]byte(`{"summary":"division","description":"Add division","acceptanceCriteria":["84 / 2 returns 42"],"repositoryURL":"https://example.test/calculator.git","sourceCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	if err != nil {
		t.Fatal(err)
	}
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-recovery", Namespace: "bootstrap-recovery", UID: "workflow-uid", Finalizers: []string{controllermeta.WorkflowFinalizer}},
		Spec: v1alpha1.SovereignWorkflowSpec{
			WorkflowID: "bootstrap-recovery",
			Bootstrap:  v1alpha1.WorkflowBootstrapSpec{SourceRef: v1alpha1.UIDReference{Name: "input", UID: "input-uid"}, Key: "request.json", ExpectedDigest: artifactcontract.DigestBytes(content), Contract: v1alpha1.ContractReference{Name: "change-request", Version: "v1"}, ArtifactName: "change-request"},
			Steps:      []v1alpha1.StepConfig{{Name: "architect", Kind: v1alpha1.ExecutionKindAgent, Agent: &v1alpha1.AgentStepSpec{Responsibility: "plan", Image: "agent", Executable: []string{"/agent"}}}},
		},
		Status: v1alpha1.SovereignWorkflowStatus{Phase: string(v1alpha1.PhasePending), PvcName: "workspace", WorkspaceWriterLeaseRef: "writer", BootstrapWriterEpoch: 1},
	}
	workflow.Status.BootstrapJobRef = controllers.BootstrapJobName(workflow.Name)
	holder, err := controllers.WorkspaceWriterIdentity("WorkflowBootstrap", workflow)
	if err != nil {
		t.Fatal(err)
	}
	grant := controllers.WorkspaceWriterGrant{LeaseName: "writer", HolderIdentity: holder, Epoch: 1}
	controller := true
	epoch := int32(1)
	immutable := true
	owner := metav1.OwnerReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "SovereignWorkflow", Name: workflow.Name, UID: workflow.UID, Controller: &controller}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: "writer", Namespace: workflow.Namespace, OwnerReferences: []metav1.OwnerReference{owner}}, Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseTransitions: &epoch}}
	job := controllers.BuildBootstrapJob(workflow, "bootstrap:test", grant)
	job.OwnerReferences = []metav1.OwnerReference{owner}
	job.Status = status
	source := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "input", Namespace: workflow.Namespace, UID: "input-uid"}, Immutable: &immutable, BinaryData: map[string][]byte{"request.json": content}}
	kube := &bootstrapArtifactFailureClient{Client: &controllerUIDClient{Client: fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.Artifact{}, &batchv1.Job{}, &v1alpha1.StepAttempt{}).
		WithObjects(workflow, source, lease, job, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: workflow.Namespace}}).Build()}}
	return &WorkflowReconciler{Client: kube, Scheme: scheme, Audit: audit.NewMemoryRecorder()}, kube, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(workflow)}, grant
}

func TestBootstrapRetriesArtifactCreationWithoutLosingWriter(t *testing.T) {
	r, kube, request, grant := bootstrapWriterFixture(t, batchv1.JobStatus{Succeeded: 1})
	kube.failures = 1
	if _, err := r.Reconcile(context.Background(), request); err == nil {
		t.Fatal("expected injected artifact creation failure")
	}
	var lease coordinationv1.Lease
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: request.Namespace, Name: grant.LeaseName}, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != grant.HolderIdentity {
		t.Fatal("writer released before Artifact creation was durable")
	}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	assertBootstrapArtifactCreatedAndWriterReleased(t, kube, request, grant)
}

func TestBootstrapResumesCompletedJobAfterWriterRelease(t *testing.T) {
	for _, releaseRecorded := range []bool{false, true} {
		t.Run(fmt.Sprint(releaseRecorded), func(t *testing.T) {
			r, kube, request, grant := bootstrapWriterFixture(t, batchv1.JobStatus{Succeeded: 1})
			if err := controllers.ReleaseWorkspaceWriter(context.Background(), kube, request.Namespace, grant, time.Now()); err != nil {
				t.Fatal(err)
			}
			var workflow v1alpha1.SovereignWorkflow
			if err := kube.Get(context.Background(), request.NamespacedName, &workflow); err != nil {
				t.Fatal(err)
			}
			workflow.Status.BootstrapWriterReleased = releaseRecorded
			if err := kube.Status().Update(context.Background(), &workflow); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			assertBootstrapArtifactCreatedAndWriterReleased(t, kube, request, grant)
		})
	}
}

func TestBootstrapJobFailureRemainsOriginalTerminalFailure(t *testing.T) {
	r, kube, request, _ := bootstrapWriterFixture(t, batchv1.JobStatus{Failed: 1})
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		var workflow v1alpha1.SovereignWorkflow
		if err := kube.Get(context.Background(), request.NamespacedName, &workflow); err != nil {
			t.Fatal(err)
		}
		condition := apiMeta.FindStatusCondition(workflow.Status.Conditions, "Ready")
		if workflow.Status.Phase != string(v1alpha1.PhaseFailed) || condition == nil || condition.Reason != "BootstrapJobFailed" {
			t.Fatalf("reconcile %d overwrote bootstrap failure: %+v", i, workflow.Status)
		}
	}
}

func TestBootstrapRunningJobStillRequiresExactWriter(t *testing.T) {
	r, kube, request, grant := bootstrapWriterFixture(t, batchv1.JobStatus{Active: 1})
	if err := controllers.ReleaseWorkspaceWriter(context.Background(), kube, request.Namespace, grant, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var workflow v1alpha1.SovereignWorkflow
	if err := kube.Get(context.Background(), request.NamespacedName, &workflow); err != nil {
		t.Fatal(err)
	}
	condition := apiMeta.FindStatusCondition(workflow.Status.Conditions, "Ready")
	if condition == nil || condition.Reason != "BootstrapWriterLost" {
		t.Fatal("running job bypassed writer fencing")
	}
	var artifact v1alpha1.Artifact
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: request.Namespace, Name: "change-request"}, &artifact); !apierrors.IsNotFound(err) {
		t.Fatalf("running job produced an Artifact: %v", err)
	}
}

func TestBootstrapDoesNotReleaseWhileJobIsActive(t *testing.T) {
	r, kube, request, grant := bootstrapWriterFixture(t, batchv1.JobStatus{Active: 1, Succeeded: 1})
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var lease coordinationv1.Lease
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: request.Namespace, Name: grant.LeaseName}, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != grant.HolderIdentity {
		t.Fatal("active job lost writer")
	}
	var artifact v1alpha1.Artifact
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: request.Namespace, Name: "change-request"}, &artifact); !apierrors.IsNotFound(err) {
		t.Fatalf("active job produced an Artifact: %v", err)
	}
}

func assertBootstrapArtifactCreatedAndWriterReleased(t *testing.T, kube client.Client, request ctrl.Request, grant controllers.WorkspaceWriterGrant) {
	t.Helper()
	var artifact v1alpha1.Artifact
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: request.Namespace, Name: "change-request"}, &artifact); err != nil {
		t.Fatalf("completed bootstrap was not resumed: %v", err)
	}
	var workflow v1alpha1.SovereignWorkflow
	if err := kube.Get(context.Background(), request.NamespacedName, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.Status.Phase == string(v1alpha1.PhaseFailed) || !workflow.Status.BootstrapWriterReleased {
		t.Fatalf("unexpected workflow state: %+v", workflow.Status)
	}
	var lease coordinationv1.Lease
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: request.Namespace, Name: grant.LeaseName}, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != "" || lease.Spec.LeaseTransitions == nil || *lease.Spec.LeaseTransitions != grant.Epoch {
		t.Fatal("completion failed to release the original epoch or reacquired a new one")
	}
	assertNoStepAttempts(t, context.Background(), kube)
}
