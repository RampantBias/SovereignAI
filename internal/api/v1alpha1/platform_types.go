package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type ResourcePhase string

const (
	PhasePending          ResourcePhase = "Pending"
	PhaseAdmitted         ResourcePhase = "Admitted"
	PhasePreparing        ResourcePhase = "Preparing"
	PhaseRunning          ResourcePhase = "Running"
	PhaseCollecting       ResourcePhase = "Collecting"
	PhaseAwaitingApproval ResourcePhase = "AwaitingApproval"
	PhaseIntervening      ResourcePhase = "Intervening"
	PhaseValidating       ResourcePhase = "Validating"
	PhaseRetrying         ResourcePhase = "Retrying"
	PhaseSucceeded        ResourcePhase = "Succeeded"
	PhaseFailed           ResourcePhase = "Failed"
	PhaseCancelled        ResourcePhase = "Cancelled"
	PhaseInterrupted      ResourcePhase = "Interrupted"
)

type ExecutionKind string

const (
	ExecutionKindAgent      ExecutionKind = "Agent"
	ExecutionKindUtility    ExecutionKind = "Utility"
	ExecutionKindHumanGate  ExecutionKind = "HumanGate"
	ExecutionKindValidation ExecutionKind = "Validation"
)

type SharingScope string

const (
	SharingDedicated            SharingScope = "Dedicated"
	SharingWithinWorkflow       SharingScope = "SharedWithinWorkflow"
	SharingWithinProject        SharingScope = "SharedWithinProject"
	SharingWithinTenant         SharingScope = "SharedWithinTenant"
	SharingWithinClassification SharingScope = "SharedWithinClassification"
)

type NamespacedReference struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

type RepositorySpec struct {
	URL             string              `json:"url"`
	DefaultRevision string              `json:"defaultRevision,omitempty"`
	CredentialRef   NamespacedReference `json:"credentialRef,omitempty"`
}

type ProjectValidationSpec struct {
	Provider           string `json:"provider"`
	InfrastructureRepo string `json:"infrastructureRepository"`
	OverlayPath        string `json:"overlayPath"`
	ImageName          string `json:"imageName,omitempty"`
}

type RetentionSpec struct {
	CompletedWorkflowTTL *metav1.Duration `json:"completedWorkflowTTL,omitempty"`
	FailedWorkflowTTL    *metav1.Duration `json:"failedWorkflowTTL,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=sproject
// +kubebuilder:subresource:status
type SovereignProject struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SovereignProjectSpec   `json:"spec"`
	Status SovereignProjectStatus `json:"status,omitempty"`
}

type SovereignProjectSpec struct {
	Tenant                string                `json:"tenant"`
	ApplicationRepository RepositorySpec        `json:"applicationRepository"`
	Validation            ProjectValidationSpec `json:"validation"`
	PolicyProfileRef      string                `json:"policyProfileRef"`
	Retention             RetentionSpec         `json:"retention,omitempty"`
	TestJob               JobTemplateSpec       `json:"testJob"`
	BuildJob              JobTemplateSpec       `json:"buildJob"`
}

type SovereignProjectStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ResourcePhase      `json:"phase,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type SovereignProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SovereignProject `json:"items"`
}

type JobTemplateSpec struct {
	Image   string   `json:"image"`
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

type ContractReference struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ArtifactReference struct {
	Name   string `json:"name"`
	Digest string `json:"digest,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=attempt
type StepAttempt struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StepAttemptSpec   `json:"spec"`
	Status StepAttemptStatus `json:"status,omitempty"`
}

type StepAttemptSpec struct {
	WorkflowRef       string                `json:"workflowRef"`
	StepName          string                `json:"stepName"`
	Attempt           int32                 `json:"attempt"`
	Kind              ExecutionKind         `json:"kind"`
	Goal              string                `json:"goal"`
	Image             string                `json:"image,omitempty"`
	Executable        []string              `json:"executable,omitempty"`
	Inputs            []ArtifactReference   `json:"inputs,omitempty"`
	OutputContracts   []ContractReference   `json:"outputContracts,omitempty"`
	Capabilities      []string              `json:"capabilities,omitempty"`
	InferenceLeaseRef string                `json:"inferenceLeaseRef,omitempty"`
	Inference         *InferenceRequestSpec `json:"inference,omitempty"`
	Timeout           *metav1.Duration      `json:"timeout,omitempty"`
}

type InferenceRequestSpec struct {
	Model             string       `json:"model"`
	ModelRevision     string       `json:"modelRevision"`
	EstimatedKVRAMMiB int64        `json:"estimatedKVRAMMiB"`
	SharingScope      SharingScope `json:"sharingScope"`
	Priority          int32        `json:"priority,omitempty"`
	Evictable         bool         `json:"evictable,omitempty"`
}

type StepAttemptStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ResourcePhase      `json:"phase,omitempty"`
	PodRef             string             `json:"podRef,omitempty"`
	JobRef             string             `json:"jobRef,omitempty"`
	ResultRef          string             `json:"resultRef,omitempty"`
	FailureReason      string             `json:"failureReason,omitempty"`
	Retryable          bool               `json:"retryable,omitempty"`
	StartedAt          *metav1.Time       `json:"startedAt,omitempty"`
	CompletedAt        *metav1.Time       `json:"completedAt,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type StepAttemptList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StepAttempt `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=artifact
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="artifact spec is immutable"
type Artifact struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArtifactSpec   `json:"spec"`
	Status ArtifactStatus `json:"status,omitempty"`
}

type ArtifactSpec struct {
	WorkflowRef    string            `json:"workflowRef"`
	ProducerRef    string            `json:"producerRef"`
	Contract       ContractReference `json:"contract"`
	Digest         string            `json:"digest"`
	Path           string            `json:"path"`
	Classification string            `json:"classification,omitempty"`
	SourceRevision string            `json:"sourceRevision,omitempty"`
}

type ArtifactStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ResourcePhase      `json:"phase,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type ArtifactList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Artifact `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hsession
type HumanSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HumanSessionSpec   `json:"spec"`
	Status HumanSessionStatus `json:"status,omitempty"`
}

type HumanSessionSpec struct {
	WorkflowRef      string           `json:"workflowRef"`
	RequesterSubject string           `json:"requesterSubject"`
	ToolProfile      string           `json:"toolProfile"`
	Capabilities     []string         `json:"capabilities,omitempty"`
	TTL              *metav1.Duration `json:"ttl"`
}

type HumanSessionStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ResourcePhase      `json:"phase,omitempty"`
	PodRef             string             `json:"podRef,omitempty"`
	ServiceRef         string             `json:"serviceRef,omitempty"`
	AccessURL          string             `json:"accessURL,omitempty"`
	BeforeRevision     string             `json:"beforeRevision,omitempty"`
	AfterRevision      string             `json:"afterRevision,omitempty"`
	ExpiresAt          *metav1.Time       `json:"expiresAt,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type HumanSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HumanSession `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=vrun
type ValidationRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ValidationRunSpec   `json:"spec"`
	Status ValidationRunStatus `json:"status,omitempty"`
}

type ValidationRunSpec struct {
	WorkflowRef string `json:"workflowRef"`
	Provider    string `json:"provider"`
	Commit      string `json:"commit"`
	ImageDigest string `json:"imageDigest"`
	OverlayPath string `json:"overlayPath"`
	Destination string `json:"destination"`
}

type ValidationRunStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ResourcePhase      `json:"phase,omitempty"`
	ProviderRef        string             `json:"providerRef,omitempty"`
	AccessURL          string             `json:"accessURL,omitempty"`
	ResultArtifactRef  string             `json:"resultArtifactRef,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type ValidationRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ValidationRun `json:"items"`
}

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
	WorkflowRef       string       `json:"workflowRef"`
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

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=pprofile
// +kubebuilder:subresource:status
type PolicyProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PolicyProfileSpec   `json:"spec"`
	Status PolicyProfileStatus `json:"status,omitempty"`
}

type PolicyProfileSpec struct {
	BundleRef NamespacedReference `json:"bundleRef"`
	Revision  string              `json:"revision"`
	Query     string              `json:"query"`
}

type PolicyProfileStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ResourcePhase      `json:"phase,omitempty"`
	LoadedRevision     string             `json:"loadedRevision,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type PolicyProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolicyProfile `json:"items"`
}
