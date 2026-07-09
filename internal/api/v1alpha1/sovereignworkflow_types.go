package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

/*
SovereignWorkflow (The CRD Object)

	├── Spec (The Intent)
	│    └── Steps []StepConfig (Array of nested structs)
	└── Status (The Reality)
	     └── StepRuntime []StepRuntimeStatus (Where we track active execution data)
*/
type StepPhase string

const (
	StepPhasePending      StepPhase = "Pending"
	StepPhaseRunning      StepPhase = "Running"
	StepPhaseIntervention StepPhase = "Intervention"
	StepPhaseCompleted    StepPhase = "Completed"
	StepPhaseFailed       StepPhase = "Failed"
)

// StepConfig defines what a specific agent needs
type StepConfig struct {
	Name                    string              `json:"name"`
	Kind                    ExecutionKind       `json:"kind"`
	Image                   string              `json:"image,omitempty"`
	Goal                    string              `json:"goal"`
	Executable              []string            `json:"executable,omitempty"`
	Capabilities            []string            `json:"capabilities,omitempty"`
	Inputs                  []ArtifactReference `json:"inputs,omitempty"`
	Outputs                 []ContractReference `json:"outputs,omitempty"`
	Timeout                 *metav1.Duration    `json:"timeout,omitempty"`
	MaxAttempts             int32               `json:"maxAttempts,omitempty"`
	RequestedVRAMAllocation int64               `json:"requestedVRAMAllocation,omitempty"`
	Order                   int                 `json:"order"`
	ModelName               string              `json:"modelName,omitempty"`
	ModelRevision           string              `json:"modelRevision,omitempty"`
	SharingScope            SharingScope        `json:"sharingScope,omitempty"`
	Priority                int32               `json:"priority,omitempty"`
	Evictable               bool                `json:"evictable,omitempty"`
	Deterministic           bool                `json:"deterministic,omitempty"`
}

type StepRuntimeStatus struct {
	StepName             string    `json:"stepName"`
	Phase                StepPhase `json:"phase"`
	AssignedNode         string    `json:"assignedNode"` //
	GPUIndex             string    `json:"gpuIndex"`     // e.g., 0, 1, 2...
	PodName              string    `json:"podName"`
	InferencePodName     string    `json:"inferencePodName"`
	InferenceEndpointURL string    `json:"inferenceEndpointUrl"`
}

// SovereignWorkflowSpec defines the desired state (The user's intent)
type SovereignWorkflowSpec struct {
	ProjectName         string       `json:"projectRef"`
	WorkflowID          string       `json:"workflowId"`
	DefinitionRevision  string       `json:"definitionRevision,omitempty"`
	Classification      string       `json:"classification,omitempty"`
	Steps               []StepConfig `json:"steps"`
	RequestedVolumeSize string       `json:"requestedVolumeSize"`
}

// SovereignWorkflowStatus defines the observed state
type SovereignWorkflowStatus struct {
	Phase                   string              `json:"phase"`                      // e.g., Pending, Running, Stalled, Completed
	ActiveStepName          string              `json:"activeStep"`                 // Currently executing step
	ActiveAttemptRef        string              `json:"activeAttemptRef,omitempty"` // Reference to the current attempt
	ObservedGeneration      int64               `json:"observedGeneration,omitempty"`
	PvcName                 string              `json:"pvcName"`                 // Bound storage resource
	AllocatedNodeName       string              `json:"allocatedNode"`           // Where the GpuService placed it
	RequestedVRAMAllocation int64               `json:"requestedVRAMAllocation"` // total requested allocation in mb
	StepStatuses            []StepRuntimeStatus `json:"stepStatuses"`            // Status for each step
	Conditions              []metav1.Condition  `json:"conditions"`              // Standard K8s status conditions
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// SovereignWorkflow is the Schema for the Sovereign workflows API
type SovereignWorkflow struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SovereignWorkflowSpec   `json:"spec,omitempty"`
	Status SovereignWorkflowStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SovereignWorkflowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SovereignWorkflow `json:"items"`
}
