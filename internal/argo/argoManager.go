package argo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/SovereignAI/internal/orchestration"
	"go.yaml.in/yaml/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Group Version Resource definition for ArgoCD Applications
var appGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

func NewManager(clusterClient dynamic.Interface, cfg ClientConfig, namespace string) (orchestration.CDClient, error) {
	// Establish gRPC/REST connection and return wrapped client
	return &argoManager{
		client:    clusterClient,
		config:    cfg,
		Namespace: namespace,
	}, nil
}

func (argo *argoManager) CheckArgoCDHealth(ctx context.Context) orchestration.ComponentStatus {
	status := orchestration.ComponentStatus{Name: "ArgoCD API"}

	start := time.Now()
	_, err := argo.GetVersion(ctx)
	status.Latency = time.Since(start)

	if err != nil {
		status.Online = false
		status.Error = fmt.Errorf("argocd api unreachable: %w", err)
		return status
	}

	status.Online = true
	return status
}

func (argo *argoManager) GetVersion(ctx context.Context) (string, error) {
	_, err := argo.client.Resource(appGVR).Namespace(argo.Namespace).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return "", fmt.Errorf("failed to communicate with ArgoCD CRDs: %w", err)
	}
	return "v1alpha1", nil
}

func (argo *argoManager) GetProjectStatus(ctx context.Context, projectName string) (*orchestration.ApplicationStatus, error) {
	unstructApp, err := argo.client.Resource(appGVR).Namespace(argo.Namespace).Get(ctx, projectName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get argo app %s: %w", projectName, err)
	}

	return convertUnstructuredStatusToApplicationStatus(unstructApp)
}

// Returns Application Status for every project being managed. Joins errors for any projects that cannot be parsed.
func (argo *argoManager) ListManagedProjects(ctx context.Context) ([]*orchestration.ApplicationStatus, error) {
	// Query client for application list
	list, err := argo.client.Resource(appGVR).Namespace(argo.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=sovereign-orchestrator",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query CDClient API: %v", err)
	}

	// keep rolling list of errors, but allow other statuses to report
	var errs []error
	statuses := make([]*orchestration.ApplicationStatus, len(list.Items))

	// Attempt to convert each status to ApplicationStatus, isolate failures
	for i, item := range list.Items {
		status, err := convertUnstructuredStatusToApplicationStatus(&item)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to parse project %s status: %v", item.GetName(), err))

			// Implement degraded status for project
			statuses[i] = &orchestration.ApplicationStatus{
				Name:         item.GetName(),
				HealthStatus: "Unknown",
				SyncStatus:   "Error",
			}
		}
		statuses[i] = status
	}

	if len(errs) > 0 {
		return statuses, errors.Join(errs...)
	}
	return statuses, nil
}

// Convert unstructured application status to orchestration's ApplicationStatus type
func convertUnstructuredStatusToApplicationStatus(unstructApp *unstructured.Unstructured) (*orchestration.ApplicationStatus, error) {
	appName := unstructApp.GetName()
	// Navigate the unstructured map safely to extract status
	status, found, _ := unstructured.NestedMap(unstructApp.Object, "status")
	if !found {
		return &orchestration.ApplicationStatus{Name: appName, IsReady: false, SyncStatus: "Unknown", HealthStatus: "Missing"}, nil
	}

	syncStatus, _, _ := unstructured.NestedString(status, "sync", "status")
	healthStatus, _, _ := unstructured.NestedString(status, "health", "status")

	return &orchestration.ApplicationStatus{
		Name:         appName,
		SyncStatus:   syncStatus,
		HealthStatus: healthStatus,
		IsReady:      syncStatus == "Synced" && healthStatus == "Healthy",
	}, nil
}

func (argo *argoManager) ApplyManifests(ctx context.Context, manifestTemplates ...*orchestration.ManifestTemplate) error {
	// Apply each manifest. TODO: Need to handle cleanup under failure
	for _, manifestTemplate := range manifestTemplates {
		// Unmarshal yaml
		objectYaml := &unstructured.Unstructured{}
		err := yaml.Unmarshal(manifestTemplate.Data, &objectYaml.Object)
		if err != nil {
			return fmt.Errorf("failed to unmarshal template data for %s: %v", manifestTemplate.Name, err)
		}

		// Generate dynamic resource based on GVR
		dynamicResource := argo.client.Resource(appGVR)

		// Apply the resource directly to the Kubernetes API plane
		_, err = dynamicResource.Apply(ctx, manifestTemplate.Name, objectYaml, metav1.ApplyOptions{FieldManager: "sovereign-orchestrator"})
		if err != nil {
			return fmt.Errorf("failed to apply manifest template %s: %v", manifestTemplate.Name, err)
		}
	}
	return nil
}
