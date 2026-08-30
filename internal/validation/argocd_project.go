package validation

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/SovereignAI/internal/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var appProjectGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}

const argoResourcesFinalizer = "resources-finalizer.argocd.argoproj.io"

var _ ProjectProvisioner = (*ArgoKustomize)(nil)

func projectName(request ProjectRequest) (string, error) {
	// Project names are also used verbatim as workflow namespace prefixes and labels.
	if problems := k8svalidation.IsDNS1123Label(request.Name); len(problems) > 0 || request.UID == "" {
		return "", fmt.Errorf("project requires a DNS label name and non-empty UID")
	}
	return "sov-" + request.Name, nil
}

func desiredAppProject(request ProjectRequest, name, namespace string) *unstructured.Unstructured {
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "AppProject",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"sourceRepos":  []any{request.InfrastructureRepo},
			"destinations": []any{map[string]any{"server": "https://kubernetes.default.svc", "namespace": request.Name + "-*"}},
			"namespaceResourceWhitelist": []any{
				map[string]any{"group": "", "kind": "Service"},
				map[string]any{"group": "apps", "kind": "Deployment"},
			},
			"clusterResourceWhitelist": []any{},
		},
	}}
	project.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "sovereign-orchestrator", "sovereign-ai.io/project": request.Name})
	project.SetFinalizers([]string{argoResourcesFinalizer})
	owner := &v1alpha1.SovereignProject{ObjectMeta: metav1.ObjectMeta{Name: request.Name, UID: request.UID}}
	project.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(owner, v1alpha1.GroupVersion.WithKind("SovereignProject"))})
	return project
}

func checkProjectOwner(project *unstructured.Unstructured, request ProjectRequest) error {
	owner := metav1.GetControllerOf(project)
	if project.GetLabels()["app.kubernetes.io/managed-by"] != "sovereign-orchestrator" ||
		owner == nil || owner.APIVersion != v1alpha1.GroupVersion.String() || owner.Kind != "SovereignProject" ||
		owner.Name != request.Name || owner.UID != request.UID {
		return apierrors.NewConflict(appProjectGVR.GroupResource(), project.GetName(), fmt.Errorf("AppProject is not owned by SovereignProject %s (UID %s)", request.Name, request.UID))
	}
	return nil
}

func (a *ArgoKustomize) EnsureProject(ctx context.Context, request ProjectRequest) (string, error) {
	name, err := projectName(request)
	if err != nil {
		return "", err
	}
	if request.InfrastructureRepo == "" {
		return "", fmt.Errorf("project infrastructure repository is required")
	}
	resource := a.client.Resource(appProjectGVR).Namespace(a.namespace)
	desired := desiredAppProject(request, name, a.namespace)
	existing, err := resource.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = resource.Create(ctx, desired, metav1.CreateOptions{FieldManager: "sovereign-project"})
		if err != nil {
			return "", fmt.Errorf("create Argo AppProject: %w", err)
		}
		return name, nil
	}
	if err != nil {
		return "", fmt.Errorf("get Argo AppProject: %w", err)
	}
	if err := checkProjectOwner(existing, request); err != nil {
		return "", err
	}
	if !existing.GetDeletionTimestamp().IsZero() {
		return "", fmt.Errorf("Argo AppProject %s is terminating", name)
	}

	updated := existing.DeepCopy()
	// Replace the complete policy, using resourceVersion for concurrency safety.
	// Merging alone could retain externally-added repositories, destinations, or roles.
	updated.Object["spec"] = desired.Object["spec"]
	labels := updated.GetLabels()
	labels["sovereign-ai.io/project"] = request.Name
	updated.SetLabels(labels)
	if !slices.Contains(updated.GetFinalizers(), argoResourcesFinalizer) {
		updated.SetFinalizers(append(updated.GetFinalizers(), argoResourcesFinalizer))
	}
	if !reflect.DeepEqual(existing.Object, updated.Object) {
		if _, err := resource.Update(ctx, updated, metav1.UpdateOptions{FieldManager: "sovereign-project"}); err != nil {
			return "", fmt.Errorf("update Argo AppProject: %w", err)
		}
	}
	return name, nil
}

func (a *ArgoKustomize) DestroyProject(ctx context.Context, request ProjectRequest) (bool, error) {
	name, err := projectName(request)
	if err != nil {
		return false, err
	}
	resource := a.client.Resource(appProjectGVR).Namespace(a.namespace)
	project, err := resource.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get Argo AppProject for cleanup: %w", err)
	}
	if err := checkProjectOwner(project, request); err != nil {
		return false, err
	}
	// Include unmanaged Applications too: never remove policy while it is in use.
	applications, err := a.client.Resource(applicationGVR).Namespace(a.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("list Argo Applications for project cleanup: %w", err)
	}
	for _, application := range applications.Items {
		reference, _, err := unstructured.NestedString(application.Object, "spec", "project")
		if err != nil {
			return false, err
		}
		if reference == name {
			return false, nil
		}
	}
	if !project.GetDeletionTimestamp().IsZero() {
		return false, nil
	}
	uid, version := project.GetUID(), project.GetResourceVersion()
	err = resource.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("delete Argo AppProject: %w", err)
	}
	// Argo's finalizer must finish; a successful DELETE is not completion.
	return false, nil
}
