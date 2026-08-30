package validationrun

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/validation"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Exercises both controllers and the real Argo adapter against in-memory API
// clients. No cluster is changed; workload sync/health remains an operator test.
func TestProjectToValidationApplicationLifecycle(t *testing.T) {
	ctx := context.Background()
	var project v1alpha1.SovereignProject
	readYAMLFixture(t, filepath.Join("..", "..", "..", "resources", "ex-project.yaml"), &project)
	project.UID, project.Generation = "project-uid", 1
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "preview", Namespace: "sovereign-ai-preview", UID: "workflow-uid"},
		Spec:       v1alpha1.SovereignWorkflowSpec{Project: v1alpha1.UIDReference{Name: project.Name, UID: project.UID}},
	}
	workflowRef := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}
	attempt := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: "validation", Namespace: workflow.Namespace, UID: "attempt-uid"},
		Spec:       v1alpha1.StepAttemptSpec{WorkflowRef: workflowRef, StepName: "validation", RetryNumber: 1, Kind: v1alpha1.ExecutionKindValidation},
		Status:     v1alpha1.StepAttemptStatus{ExecutionRef: &v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ValidationRun", Name: "validation"}},
	}
	run := &v1alpha1.ValidationRun{
		ObjectMeta: metav1.ObjectMeta{Name: attempt.Name, Namespace: workflow.Namespace, Generation: 1, Finalizers: []string{ValidationFinalizer}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(attempt, v1alpha1.GroupVersion.WithKind("StepAttempt"))}},
		Spec:       v1alpha1.ValidationRunSpec{WorkflowRef: workflowRef, AttemptRef: attempt.Name, StepName: "validation", Attempt: 1},
	}
	objects := []client.Object{&project, workflow, attempt, run, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: workflow.Namespace}}}
	artifacts := validationArtifactFixtures(workflow, &project, strings.Repeat("c", 40), strings.Repeat("d", 40), "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("e", 64))
	for i := range artifacts {
		artifact := &artifacts[i]
		objects = append(objects, artifact)
		run.Spec.Inputs = append(run.Spec.Inputs, v1alpha1.ArtifactReference{Name: artifact.Spec.Contract.Name, Digest: artifact.Spec.Digest, ArtifactRef: &v1alpha1.UIDReference{Name: artifact.Name, UID: artifact.UID}})
	}
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.SovereignProject{}, &v1alpha1.ValidationRun{}).WithObjects(objects...).Build()
	appGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	projectGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}
	argo := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{appGVR: "ApplicationList", projectGVR: "AppProjectList"})
	provider := validation.NewArgoKustomize(argo, "argocd")
	projectController := &controllers.SovereignProjectReconciler{Client: kube, Provisioner: provider}
	runController := &ValidationRunReconciler{Client: kube, Provider: provider}
	runRequest := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
	result, err := runController.Reconcile(ctx, runRequest)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("run must wait for project: %#v %v", result, err)
	}
	if len(argo.Actions()) != 0 {
		t.Fatal("waiting ValidationRun must not contact Argo to provision policy")
	}
	projectRequest := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&project)}
	for range 2 {
		if _, err := projectController.Reconcile(ctx, projectRequest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runController.Reconcile(ctx, runRequest); err != nil {
		t.Fatal(err)
	}
	app, err := argo.Resource(appGVR).Namespace("argocd").Get(ctx, run.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reference, _, _ := unstructured.NestedString(app.Object, "spec", "project")
	destination, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "namespace")
	if reference != "sov-sovereign-ai" || destination != workflow.Namespace {
		t.Fatalf("Application policy/destination mismatch: %#v", app.Object["spec"])
	}
	var latest v1alpha1.SovereignProject
	if err := kube.Get(ctx, projectRequest.NamespacedName, &latest); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(ctx, &latest); err != nil {
		t.Fatal(err)
	}
	result, err = projectController.Reconcile(ctx, projectRequest)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("project deletion must wait for Application: %#v %v", result, err)
	}
	if err := argo.Resource(appGVR).Namespace("argocd").Delete(ctx, run.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := projectController.Reconcile(ctx, projectRequest); err != nil {
			t.Fatal(err)
		}
	}
	if err := kube.Get(ctx, projectRequest.NamespacedName, &latest); !apierrors.IsNotFound(err) {
		t.Fatalf("project cleanup did not complete: %v", err)
	}
	var ns corev1.Namespace
	if err := kube.Get(ctx, client.ObjectKey{Name: workflow.Namespace}, &ns); err != nil {
		t.Fatalf("project cleanup touched workflow namespace: %v", err)
	}
}
