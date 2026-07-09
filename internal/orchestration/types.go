package orchestration

import (
	"context"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Configuration properties for the Orchestrator
type OrchestratorConfig struct {
	BaseNamespace    string // Orchestrator's namespace
	StorageClassName string

	// Image Configuration
	AgentImage      string // "sovereign-orchestrator-agent:latest" or "192.168.1.50:5000/sovereign-orchestrator-agent"
	ImagePullPolicy string // IfNotPresent, Always
}

type AIModel struct {
	Name         string
	RequiredVRAM int // MB
}

type SovereignWorkflowController struct {
	CDClient      CDClient
	Config        OrchestratorConfig
	GpuService    GpuService
	MetricsClient MetricsClient
	Client        client.Client   // K8s API interactions
	Scheme        *runtime.Scheme // CRD definitions
}

// Task represents the order details sent to the agent pod
type Task struct {
	StepName       string            `json:"step_name"`
	WorkflowID     string            `json:"workflow_id"`
	Responsibility string            `json:"responsibility"`
	Parameters     map[string]string `json:"parameters"`
	CreatedAt      time.Time         `json:"created_at"`
}

// Status represents the result sent back by the agent pod
type Status struct {
	State      string    `json:"state"`
	ExitCode   int       `json:"exit_code"`
	Message    string    `json:"message"`
	Artifacts  []string  `json:"artifacts"` // Paths to files created
	FinishedAt time.Time `json:"finished_at"`
}

type PodConfig struct {
	Namespace      string
	WorkflowID     string
	StepName       string
	RequiresReview bool
	NodeName       string

	AgentImage      string
	ImagePullPolicy string
	VRamRequirement string
	GPUID           string
}

type PvcConfig struct {
	Namespace        string
	WorkflowID       string
	Name             string
	StorageClassName string
	StorageSize      string
}

// type Namespace struct {
// 	Name      string
// 	Labels    map[string]string
// 	CreatedAt time.Time
// }

// ****************
// Pvc
// ****************
// type PvcStatus int

// const (
// 	PvcPending PvcStatus = iota
// 	PvcBound
// 	PvcLost
// )

// type Pvc struct {
// 	Name   string
// 	Status PvcStatus
// }

// Used for orchestrator's primary component status (Kubernetes/ArgoCD)
type ComponentStatus struct {
	Name    string        `json:"name"`
	Online  bool          `json:"online"`
	Latency time.Duration `json:"latency"`
	Error   error         `json:"error,omitempty"`
}

type PreFlightReport struct {
	IsHealthy bool              `json:"is_healthy"`
	Details   []ComponentStatus `json:"details"`
}

// Used for orchestrator application status (Prometheus/Grafana/etc)
type ApplicationStatus struct {
	Name         string
	SyncStatus   string
	HealthStatus string
	IsReady      bool
}

// ManifestTemplate allows transfer of agnostic manifests
type ManifestTemplate struct {
	Name string
	Data []byte
}

/*****************************************
************GPU-Supporting****************
*****************************************/
type NodeSpec struct {
	Name    string
	IsReady bool
	GPUs    []GPUSpec
}

type GPUSpec struct {
	Index     int
	Model     string
	TotalVRAM int64 // Always use int64 for bytes/MB to handle precise allocations
}

type Allocation struct {
	WorkflowID   string
	StepName     string
	ReservedVRAM int64 // Weights + baseline footprint
}

type AllocationRequest struct {
	WorkflowID        string
	RequiredModelVRAM int64 //static
	RequiredKVVRAM    int64 //dynamic estimate
	ModelName         string
}

type PlacementResult struct {
	NodeName         string
	GPUIndex         string
	EndpointURL      string
	InferencePodName string
	Action           string // Reuse or Create
}

type StepContext struct {
	Ctx context.Context
	wf  v1alpha1.SovereignWorkflow
}
