package validation

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var applicationGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

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
	if request.ImageName != "" && request.ImageDigest != "" {
		source["kustomize"] = map[string]any{"images": []any{request.ImageName + "=" + request.ImageDigest}}
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
	resource := a.client.Resource(applicationGVR).Namespace(a.namespace)
	_, err := resource.Apply(ctx, request.Name, application, metav1.ApplyOptions{FieldManager: "sovereign-validation", Force: true})
	if apierrors.IsNotFound(err) {
		_, err = resource.Create(ctx, application, metav1.CreateOptions{})
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
	result := Status{Phase: healthStatus, Ready: syncStatus == "Synced" && healthStatus == "Healthy", Failed: healthStatus == "Degraded" || healthStatus == "Missing", AccessURL: "/applications/" + reference}
	return result, nil
}

func (a *ArgoKustomize) Destroy(ctx context.Context, reference string) error {
	propagation := metav1.DeletePropagationForeground
	err := a.client.Resource(applicationGVR).Namespace(a.namespace).Delete(ctx, reference, metav1.DeleteOptions{PropagationPolicy: &propagation})
	return err
}
