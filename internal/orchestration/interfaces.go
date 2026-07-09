package orchestration

import (
	"context"
)

// Cluster Manager represents the Kubernetes API/Interface
// type ClusterManager interface {
// 	// Checks if cluster is healthy and API is responsive
// 	CheckKubernetesHealth(ctx context.Context) ComponentStatus

// 	GetNodes(ctx context.Context) ([]NodeSpec, error)

// 	GetWorkflows(ctx context.Context) (v1alpha1.SovereignWorkflowList, error)

// 	CreateNamespace(ctx context.Context, name, projectName string) error
// 	GetNamespaces(ctx context.Context) ([]Namespace, error)
// 	CleanupNamespace(ctx context.Context, namespace string) error

// 	CreateAgentPod(ctx context.Context, podConfig PodConfig) error
// 	GetPodStatus(ctx context.Context, namespace string, podName string) (string, error)
// 	GetPods(ctx context.Context, namespace string) ([]Pod, error)

// 	CreateAgentRBAC(ctx context.Context, namespace string) error
// 	CreateAgentNetworkPolicy(ctx context.Context, namespace string) error

// 	GetPVCs(ctx context.Context, namespace string) ([]Pvc, error)
// 	CreatePVC(ctx context.Context, config PvcConfig) error
// 	GetPVCPath(ctx context.Context, namespace string) string
// 	//GetPVCStatus(ctx context.Context, workflow *core.Workflow) (PvcStatus, error)
// 	GetHostPath(ctx context.Context, ns, pvcName string) (string, error)
// }

// CD Client is responsible for Continuous Deployment of Orchestrator Infrastructure & Ephemeral Test Infrastructure
type CDClient interface {
	GetVersion(ctx context.Context) (string, error)
	CheckArgoCDHealth(ctx context.Context) ComponentStatus
	GetProjectStatus(ctx context.Context, projectName string) (*ApplicationStatus, error)
	ListManagedProjects(ctx context.Context) ([]*ApplicationStatus, error)

	RegisterProject(ctx context.Context, name, infraRepo, appRepo string) error
	ApplyManifests(ctx context.Context, manifestTemplates ...*ManifestTemplate) error
}

// Metrics Client
type MetricsClient interface {
	Ping(ctx context.Context) error
	QueryActiveGPUCount(ctx context.Context) (int, error)
}

// GPU Service is responsible for scheduling requests & GPU telemetry retrieval
type GpuService interface {
	// FindPlacement evaluates the cluster and returns the best Node/GPU for a new request.
	FindPlacement(ctx context.Context, req AllocationRequest) (PlacementResult, error)

	// ReleaseAllocation removes a subscription when a workflow or step completes/terminates.
	ReleaseAllocation(ctx context.Context, nodeName string, gpuIndex int, workflowID string) error
}
