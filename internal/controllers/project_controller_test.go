package controllers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/validation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"
)

type projectProvisionerStub struct {
	ensureCalls  int
	destroyCalls int
	done         bool
	err          error
	last         validation.ProjectRequest
	onEnsure     func()
}

func (p *projectProvisionerStub) EnsureProject(_ context.Context, request validation.ProjectRequest) (string, error) {
	p.ensureCalls++
	p.last = request
	if p.onEnsure != nil {
		p.onEnsure()
	}
	return "sov-" + request.Name, p.err
}

func (p *projectProvisionerStub) DestroyProject(_ context.Context, request validation.ProjectRequest) (bool, error) {
	p.destroyCalls++
	p.last = request
	return p.done, p.err
}

func validProjectFixture(t *testing.T) *v1alpha1.SovereignProject {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "ex-project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var project v1alpha1.SovereignProject
	if err := yaml.UnmarshalStrict(data, &project); err != nil {
		t.Fatal(err)
	}
	project.UID, project.Generation = "project-uid", 1
	if problems := ValidateSovereignProjectSpec(&project); len(problems) != 0 {
		t.Fatalf("MVP manifest must remain valid: %v", problems)
	}
	return &project
}

func projectReconciler(t *testing.T, project *v1alpha1.SovereignProject, provisioner validation.ProjectProvisioner) *SovereignProjectReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.SovereignProject{}).WithObjects(project).Build()
	return &SovereignProjectReconciler{Client: kube, Provisioner: provisioner}
}

func readProject(t *testing.T, kube client.Client, key client.ObjectKey) *v1alpha1.SovereignProject {
	t.Helper()
	var project v1alpha1.SovereignProject
	if err := kube.Get(context.Background(), key, &project); err != nil {
		t.Fatal(err)
	}
	return &project
}

func TestProjectProvisioningLifecycle(t *testing.T) {
	ctx := context.Background()
	project := validProjectFixture(t)
	stub := &projectProvisionerStub{}
	r := projectReconciler(t, project, stub)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(project)}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	updated := readProject(t, r.Client, request.NamespacedName)
	if stub.ensureCalls != 0 || !controllerutil.ContainsFinalizer(updated, ProjectFinalizer) {
		t.Fatal("cleanup finalizer must be persisted before external provisioning")
	}
	result, err := r.Reconcile(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	updated = readProject(t, r.Client, request.NamespacedName)
	if !v1alpha1.ProjectReady(updated) || updated.Status.ValidationProviderRef != "sov-sovereign-ai" {
		t.Fatalf("project not ready: %#v", updated.Status)
	}
	if stub.last.UID != project.UID || stub.last.InfrastructureRepo != project.Spec.Validation.InfrastructureRepo {
		t.Fatalf("provisioning lost owner/policy: %#v", stub.last)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Fatalf("missing periodic drift check: %#v", result)
	}
	version := updated.ResourceVersion
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if readProject(t, r.Client, request.NamespacedName).ResourceVersion != version {
		t.Fatal("unchanged conditions must not churn status")
	}

	updated.Generation++
	updated.Spec.Validation.InfrastructureRepo = "https://git.example.test/updated.git"
	if err := r.Update(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if v1alpha1.ProjectReady(updated) {
		t.Fatal("old readiness must not authorize a new generation")
	}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	updated = readProject(t, r.Client, request.NamespacedName)
	if !v1alpha1.ProjectReady(updated) || stub.last.InfrastructureRepo != updated.Spec.Validation.InfrastructureRepo {
		t.Fatal("new generation not provisioned")
	}
}

func TestProjectInvalidConfigurationNeverProvisions(t *testing.T) {
	project := validProjectFixture(t)
	// The existing gRPC CreateProject path also uses main and omits these fields.
	project.Spec.ApplicationRepository.DefaultRevision = "main"
	project.Spec.Validation.ImageName = ""
	project.Spec.BuildJob = v1alpha1.JobTemplateSpec{}
	stub := &projectProvisionerStub{}
	r := projectReconciler(t, project, stub)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(project)}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	updated := readProject(t, r.Client, request.NamespacedName)
	if stub.ensureCalls != 0 || controllerutil.ContainsFinalizer(updated, ProjectFinalizer) || v1alpha1.ProjectReady(updated) {
		t.Fatal("invalid project gained external policy")
	}
	for _, kind := range []string{ProjectConditionConfigurationValid, ProjectConditionValidationProviderReady} {
		condition := apiMeta.FindStatusCondition(updated.Status.Conditions, kind)
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.ObservedGeneration != project.Generation {
			t.Fatalf("missing invalid status: %#v", condition)
		}
	}
}

func TestProjectProvisioningFailureClearsReadiness(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			project := validProjectFixture(t)
			project.Finalizers = []string{ProjectFinalizer}
			project.Status.ValidationProviderRef = "sov-sovereign-ai"
			project.Status.Conditions = []metav1.Condition{{Type: ProjectConditionValidationProviderReady, Status: metav1.ConditionTrue, Reason: "AppProjectReady", ObservedGeneration: project.Generation}}
			stub := &projectProvisionerStub{err: fmt.Errorf("Argo unavailable")}
			reason := "AppProjectProvisioningFailed"
			if conflict {
				stub.err = apierrors.NewConflict(schema.GroupResource{Group: "argoproj.io", Resource: "appprojects"}, "sov-sovereign-ai", fmt.Errorf("other owner"))
				reason = "AppProjectConflict"
			}
			r := projectReconciler(t, project, stub)
			key := client.ObjectKeyFromObject(project)
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err == nil {
				t.Fatal("operational error must requeue")
			}
			updated := readProject(t, r.Client, key)
			condition := apiMeta.FindStatusCondition(updated.Status.Conditions, ProjectConditionValidationProviderReady)
			if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != reason || v1alpha1.ProjectReady(updated) {
				t.Fatalf("unexpected status: %#v", updated.Status)
			}
		})
	}
}

func TestProjectCleanupWaitsAndRetainsIdentityWithoutStatusRef(t *testing.T) {
	ctx := context.Background()
	project := validProjectFixture(t)
	project.Finalizers = []string{ProjectFinalizer, "example.test/other-cleanup"}
	now := metav1.Now()
	project.DeletionTimestamp = &now
	stub := &projectProvisionerStub{}
	r := projectReconciler(t, project, stub)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(project)}
	result, err := r.Reconcile(ctx, request)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("cleanup must wait: %#v %v", result, err)
	}
	if stub.ensureCalls != 0 || stub.last.UID != project.UID {
		t.Fatal("cleanup must use durable owner identity even without a saved reference")
	}
	if !controllerutil.ContainsFinalizer(readProject(t, r.Client, request.NamespacedName), ProjectFinalizer) {
		t.Fatal("removed finalizer too soon")
	}
	stub.err = fmt.Errorf("unavailable")
	if _, err := r.Reconcile(ctx, request); err == nil {
		t.Fatal("cleanup error must be retried")
	}
	if !controllerutil.ContainsFinalizer(readProject(t, r.Client, request.NamespacedName), ProjectFinalizer) {
		t.Fatal("cleanup error lost finalizer")
	}
	stub.err, stub.done = nil, true
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	updated := readProject(t, r.Client, request.NamespacedName)
	if controllerutil.ContainsFinalizer(updated, ProjectFinalizer) || !controllerutil.ContainsFinalizer(updated, "example.test/other-cleanup") {
		t.Fatal("cleanup must remove only its own finalizer after absence")
	}
}

func TestProjectGenerationRaceCannotPublishReady(t *testing.T) {
	project := validProjectFixture(t)
	project.Finalizers = []string{ProjectFinalizer}
	stub := &projectProvisionerStub{}
	r := projectReconciler(t, project, stub)
	key := client.ObjectKeyFromObject(project)
	stub.onEnsure = func() {
		latest := readProject(t, r.Client, key)
		latest.Generation++
		if err := r.Update(context.Background(), latest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err == nil {
		t.Fatal("spec change must trigger fresh reconciliation")
	}
	if v1alpha1.ProjectReady(readProject(t, r.Client, key)) {
		t.Fatal("provisioning old policy marked new spec ready")
	}
}

func TestProjectWithoutProvisionerIsNotReady(t *testing.T) {
	project := validProjectFixture(t)
	r := projectReconciler(t, project, nil)
	key := client.ObjectKeyFromObject(project)
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("missing provider must remain pending: %#v %v", result, err)
	}
	if v1alpha1.ProjectReady(readProject(t, r.Client, key)) {
		t.Fatal("missing provider cannot be ready")
	}
}
