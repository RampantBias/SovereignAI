package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=iendpoint
type InferenceEndpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InferenceEndpointSpec   `json:"spec"`
	Status InferenceEndpointStatus `json:"status,omitempty"`
}

type InferenceEndpointSpec struct {
	Provider          string           `json:"provider"`
	Model             string           `json:"model"`
	ModelRevision     string           `json:"modelRevision"`
	RuntimeImage      string           `json:"runtimeImage"`
	Tenant            string           `json:"tenant"`
	Classification    string           `json:"classification"`
	SharingScope      SharingScope     `json:"sharingScope"`
	StaticVRAMMiB     int64            `json:"staticVRAMMiB"`
	MaxKVRAMMiB       int64            `json:"maxKVRAMMiB"`
	SafetyHeadroomMiB int64            `json:"safetyHeadroomMiB,omitempty"`
	IdleTTL           *metav1.Duration `json:"idleTTL,omitempty"`
}

type InferenceEndpointStatus struct {
	ObservedGeneration int64                 `json:"observedGeneration,omitempty"`
	Phase              ResourcePhase         `json:"phase,omitempty"`
	PodRef             string                `json:"podRef,omitempty"`
	ServiceRef         string                `json:"serviceRef,omitempty"`
	NodeName           string                `json:"nodeName,omitempty"`
	DeviceName         string                `json:"deviceName,omitempty"`
	AllocatedKVRAMMiB  int64                 `json:"allocatedKVRAMMiB,omitempty"`
	ActiveLeaseCount   int32                 `json:"activeLeaseCount,omitempty"`
	ActiveLeases       []NamespacedReference `json:"activeLeases,omitempty"`
	LastUsedAt         *metav1.Time          `json:"lastUsedAt,omitempty"`
	Conditions         []metav1.Condition    `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type InferenceEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InferenceEndpoint `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ilease
type InferenceLease struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InferenceLeaseSpec   `json:"spec"`
	Status InferenceLeaseStatus `json:"status,omitempty"`
}

type InferenceLeaseSpec struct {
	WorkflowRef       UIDReference `json:"workflowRef"`
	AttemptRef        string       `json:"attemptRef"`
	ProjectRef        string       `json:"projectRef"`
	Tenant            string       `json:"tenant"`
	Classification    string       `json:"classification"`
	SharingScope      SharingScope `json:"sharingScope"`
	Model             string       `json:"model"`
	ModelRevision     string       `json:"modelRevision"`
	EstimatedKVRAMMiB int64        `json:"estimatedKVRAMMiB"`
	Priority          int32        `json:"priority,omitempty"`
	Evictable         bool         `json:"evictable,omitempty"`
}

type InferenceLeaseStatus struct {
	ObservedGeneration int64               `json:"observedGeneration,omitempty"`
	Phase              ResourcePhase       `json:"phase,omitempty"`
	EndpointRef        NamespacedReference `json:"endpointRef,omitempty"`
	EndpointURL        string              `json:"endpointURL,omitempty"`
	DecisionID         string              `json:"decisionID,omitempty"`
	Reason             string              `json:"reason,omitempty"`
	Conditions         []metav1.Condition  `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type InferenceLeaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InferenceLease `json:"items"`
}
