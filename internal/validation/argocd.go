package validation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var applicationGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// Including the run UID isolates retries, workflows and recreated resources.
func ApplicationName(namespace, name, uid string) string {
	return fmt.Sprintf("validation-%x", sha256.Sum256([]byte(namespace+"/"+name+"/"+uid)))[:63]
}

type ArgoKustomize struct {
	client    dynamic.Interface
	namespace string
}

func NewArgoKustomize(client dynamic.Interface, namespace string) *ArgoKustomize {
	return &ArgoKustomize{client: client, namespace: namespace}
}

func (a *ArgoKustomize) Start(ctx context.Context, request Request) (string, error) {
	source := map[string]any{
		"repoURL": request.InfrastructureRepo, "targetRevision": request.InfrastructureRevision, "path": request.OverlayPath,
	}
	if request.ImageSelector != "" && request.ImageDigest != "" {
		source["kustomize"] = map[string]any{"images": []any{request.ImageSelector + "=" + request.ImageDigest}}
	}
	application := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": request.Name, "namespace": a.namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "sovereign-orchestrator", "sovereign-ai.io/workflow-namespace": request.WorkflowNamespace}},
		"spec": map[string]any{
			"project": request.Project, "source": source,
			"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": request.WorkflowNamespace},
			"syncPolicy":  map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true}},
		},
	}}
	application.SetFinalizers([]string{argoResourcesFinalizer})
	resource := a.client.Resource(applicationGVR).Namespace(a.namespace)
	existing, err := resource.Get(ctx, request.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = resource.Create(ctx, application, metav1.CreateOptions{})
	} else if err == nil {
		if existing.GetLabels()["app.kubernetes.io/managed-by"] != "sovereign-orchestrator" || existing.GetLabels()["sovereign-ai.io/workflow-namespace"] != request.WorkflowNamespace {
			return "", fmt.Errorf("Argo Application %s belongs to another owner", request.Name)
		}
		if !existing.GetDeletionTimestamp().IsZero() {
			return "", fmt.Errorf("Argo Application %s is terminating", request.Name)
		}
		existing.Object["spec"] = application.Object["spec"]
		if !slices.Contains(existing.GetFinalizers(), argoResourcesFinalizer) {
			existing.SetFinalizers(append(existing.GetFinalizers(), argoResourcesFinalizer))
		}
		_, err = resource.Update(ctx, existing, metav1.UpdateOptions{})
	}
	if err != nil {
		return "", fmt.Errorf("apply Argo CD validation application: %w", err)
	}
	return request.Name, nil
}

func (a *ArgoKustomize) Status(ctx context.Context, reference string) (Status, error) {
	application, err := a.client.Resource(applicationGVR).Namespace(a.namespace).Get(ctx, reference, metav1.GetOptions{})
	if err != nil {
		return Status{}, fmt.Errorf("get Argo CD validation application: %w", err)
	}
	syncStatus, _, _ := unstructured.NestedString(application.Object, "status", "sync", "status")
	healthStatus, _, _ := unstructured.NestedString(application.Object, "status", "health", "status")
	result := Status{
		Phase:        healthStatus,
		SyncStatus:   syncStatus,
		HealthStatus: healthStatus,
		Ready:        application.GetDeletionTimestamp().IsZero() && syncStatus == "Synced" && healthStatus == "Healthy",
		Failed:       healthStatus == "Degraded",
		AccessURL:    "/applications/" + reference}
	conditions, _, _ := unstructured.NestedSlice(application.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _, _ := unstructured.NestedString(condition, "type")
		switch conditionType {
		case "ComparisonError", "InvalidSpecError", "SyncError", "DeletionError":
			message, _, _ := unstructured.NestedString(condition, "message")
			result.Phase = conditionType
			result.Ready = false
			result.Failed = true
			result.Message = message
			if result.Message == "" {
				result.Message = "Argo CD reported " + conditionType
			}
			return result, nil
		}
	}
	if result.Failed && result.Message == "" {
		result.Message = "Argo CD application health is Degraded"
	}
	return result, nil
}

func (a *ArgoKustomize) Destroy(ctx context.Context, reference string) error {
	resource := a.client.Resource(applicationGVR).Namespace(a.namespace)
	application, err := resource.Get(ctx, reference, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if application.GetLabels()["app.kubernetes.io/managed-by"] != "sovereign-orchestrator" {
		return fmt.Errorf("refusing to delete unmanaged Argo Application %s", reference)
	}
	if !application.GetDeletionTimestamp().IsZero() {
		return nil
	}
	propagation := metav1.DeletePropagationForeground
	uid := application.GetUID()
	err = resource.Delete(
		ctx,
		reference,
		metav1.DeleteOptions{
			PropagationPolicy: &propagation,
			Preconditions:     &metav1.Preconditions{UID: &uid}})
	return err
}
