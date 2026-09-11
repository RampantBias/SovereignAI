package validation

import (
	"context"
	"encoding/base64"
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

var (
	appProjectGVR       = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}
	repositorySecretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
)

const (
	argoResourcesFinalizer        = "resources-finalizer.argocd.argoproj.io"
	argoRepositorySecretTypeLabel = "argocd.argoproj.io/secret-type"
)

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

func repositoryCredentialName(projectName string) string {
	return projectName + "-repository"
}

func desiredRepositoryCredential(request ProjectRequest, projectName, namespace string) *unstructured.Unstructured {
	encode := func(value string) string {
		return base64.StdEncoding.EncodeToString([]byte(value))
	}
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      repositoryCredentialName(projectName),
			"namespace": namespace,
		},
		"type": "Opaque",
		"data": map[string]any{
			"type":     encode("git"),
			"url":      encode(request.InfrastructureRepo),
			"username": encode(request.RepositoryCredential.Username),
			"password": encode(request.RepositoryCredential.Password),
			"project":  encode(projectName),
		},
	}}
	secret.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by": "sovereign-orchestrator",
		"sovereign-ai.io/project":      request.Name,
		argoRepositorySecretTypeLabel:  "repository",
	})
	owner := &v1alpha1.SovereignProject{ObjectMeta: metav1.ObjectMeta{Name: request.Name, UID: request.UID}}
	secret.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(owner, v1alpha1.GroupVersion.WithKind("SovereignProject"))})
	return secret
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

func checkRepositoryCredentialOwner(secret *unstructured.Unstructured, request ProjectRequest) error {
	owner := metav1.GetControllerOf(secret)
	if secret.GetLabels()["app.kubernetes.io/managed-by"] != "sovereign-orchestrator" ||
		owner == nil || owner.APIVersion != v1alpha1.GroupVersion.String() || owner.Kind != "SovereignProject" ||
		owner.Name != request.Name || owner.UID != request.UID {
		return apierrors.NewConflict(repositorySecretGVR.GroupResource(), secret.GetName(), fmt.Errorf("repository credential is not owned by SovereignProject %s (UID %s)", request.Name, request.UID))
	}
	return nil
}

func (a *ArgoKustomize) ensureRepositoryCredential(ctx context.Context, request ProjectRequest, projectName string) error {
	resource := a.client.Resource(repositorySecretGVR).Namespace(a.namespace)
	desired := desiredRepositoryCredential(request, projectName, a.namespace)
	existing, err := resource.Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := resource.Create(ctx, desired, metav1.CreateOptions{FieldManager: "sovereign-project"}); err != nil {
			return fmt.Errorf("create Argo repository credential: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get Argo repository credential: %w", err)
	}
	if err := checkRepositoryCredentialOwner(existing, request); err != nil {
		return err
	}
	if !existing.GetDeletionTimestamp().IsZero() {
		return fmt.Errorf("Argo repository credential %s is terminating", existing.GetName())
	}

	updated := existing.DeepCopy()
	updated.Object["type"] = desired.Object["type"]
	updated.Object["data"] = desired.Object["data"]
	delete(updated.Object, "stringData")
	labels := updated.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	for key, value := range desired.GetLabels() {
		labels[key] = value
	}
	updated.SetLabels(labels)
	if reflect.DeepEqual(existing.Object, updated.Object) {
		return nil
	}
	if _, err := resource.Update(ctx, updated, metav1.UpdateOptions{FieldManager: "sovereign-project"}); err != nil {
		return fmt.Errorf("update Argo repository credential: %w", err)
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
	if request.RepositoryCredential.Username == "" || request.RepositoryCredential.Password == "" {
		return "", fmt.Errorf("project repository credential is required")
	}
	resource := a.client.Resource(appProjectGVR).Namespace(a.namespace)
	desired := desiredAppProject(request, name, a.namespace)
	existing, err := resource.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = resource.Create(ctx, desired, metav1.CreateOptions{FieldManager: "sovereign-project"})
		if err != nil {
			return "", fmt.Errorf("create Argo AppProject: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("get Argo AppProject: %w", err)
	} else {
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
	}
	if err := a.ensureRepositoryCredential(ctx, request, name); err != nil {
		return "", err
	}
	return name, nil
}

func (a *ArgoKustomize) DestroyProject(ctx context.Context, request ProjectRequest) (bool, error) {
	name, err := projectName(request)
	if err != nil {
		return false, fmt.Errorf("delete Argo AppProject: %w", err)
	}
	projectResource := a.client.Resource(appProjectGVR).Namespace(a.namespace)
	project, err := projectResource.Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("get Argo AppProject for cleanup: %w", err)
	}
	projectExists := err == nil
	if projectExists {
		if err := checkProjectOwner(project, request); err != nil {
			return false, err
		}
	}

	// Keep repository access available until every Application using the project
	// is gone, so Argo can finish pruning preview resources.
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

	credentialResource := a.client.Resource(repositorySecretGVR).Namespace(a.namespace)
	credentialName := repositoryCredentialName(name)
	credential, credentialErr := credentialResource.Get(ctx, credentialName, metav1.GetOptions{})
	if credentialErr != nil && !apierrors.IsNotFound(credentialErr) {
		return false, fmt.Errorf("get Argo repository credential for cleanup: %w", credentialErr)
	}
	if credentialErr == nil {
		if err := checkRepositoryCredentialOwner(credential, request); err != nil {
			return false, err
		}
		if !credential.GetDeletionTimestamp().IsZero() {
			return false, nil
		}
		uid, version := credential.GetUID(), credential.GetResourceVersion()
		if err := credentialResource.Delete(ctx, credentialName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete Argo repository credential: %w", err)
		}
		return false, nil
	}

	if !projectExists {
		return true, nil
	}
	if !project.GetDeletionTimestamp().IsZero() {
		return false, nil
	}
	uid, version := project.GetUID(), project.GetResourceVersion()
	err = projectResource.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	// Argo's finalizer must finish; a successful DELETE is not completion.
	return false, nil
}
