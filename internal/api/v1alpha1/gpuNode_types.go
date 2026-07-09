package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

/*
	TODO: Expand from just GPU to CPU as well
spec:
  hardwareType: "Hybrid" # Can be GPU_Only, CPU_Only, or Hybrid
  gpuLedger:
    totalVRAM: 16384
    pressureThreshold: 90
  cpuLedger:
    totalSystemRAM: 65536
    totalCores: 16
    pressureThreshold: 85
status:
  gpuPressure: "False"
  cpuPressure: "False"
*/

type GPUSpec struct {
	NodeName  string `json:"nodeName"`
	TotalVRAM int64  `json:"totalVRAM"`
}

// TODO: Determine other event detections outside basic VRAM pressure
// Nvidia Daemonset will patch this into a CRD if a workflow exceeds allocation request
type AgentEvictionTrigger struct {
	WorkflowName string      `json:"workflowName"`
	Reason       string      `json:"reason"`
	Timestamp    metav1.Time `json:"timestamp"`
}

// LegacyInferenceEndpoint is retained only while the original GPUNode ledger
// is migrated to the standalone InferenceEndpoint and InferenceLease APIs.
type LegacyInferenceEndpoint struct {
	ModelID         string   `json:"modelID"`
	GPUIndex        string   `json:"gpuIndex"`
	EndpointURL     string   `json:"serviceName"`
	AllocatedVRAM   int64    `json:"allocatedVRAM"`
	ActiveWorkflows []string `json:"activeWorkflows"`
}

// Simplifying to just one GPU for MVP
type GPUStatus struct {
	AllocatedVRAM            int64                     `json:"allocatedVRAM"`
	UsedVRAM                 int64                     `json:"usedVRAM"`
	ActiveWorkflows          []string                  `json:"activeWorkflows"`
	HasVRAMPressure          bool                      `json:"hasVRAMPressure"`
	ActiveInferenceEndpoints []LegacyInferenceEndpoint `json:"activeInferenceEndpoints"`
	//EvictionTriggers []AgentEvictionTrigger `json:"evictionTriggers"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// SovereignWorkflow is the Schema for the Sovereign workflows API
type GPUNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GPUSpec   `json:"spec,omitempty"`
	Status GPUStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type GPUNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GPUNode `json:"items"`
}
