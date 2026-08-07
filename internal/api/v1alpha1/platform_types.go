package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type ApprovalMode string

const (
	AnyOf ApprovalMode = `AnyOf`
	//AllOf  ApprovalMode = `AllOf`
	//Quorum ApprovalMode = `Quorum`
)

type ApprovalChoice string

const (
	Approved ApprovalChoice = `Approved`
	Denied   ApprovalChoice = `Denied`
)

type Subject struct {
	SubjectId   string   `json:"subjectId"`
	Email       string   `json:"email"`
	DisplayName string   `json:"displayName"`
	Provider    string   `json:"provider"`
	Groups      []string `json:"groups"`
}

type RepositorySpec struct {
	URL             string              `json:"url"`
	DefaultRevision string              `json:"defaultRevision,omitempty"`
	CredentialRef   NamespacedReference `json:"credentialRef,omitempty"`
}

type RetentionSpec struct {
	CompletedWorkflowTTL *metav1.Duration `json:"completedWorkflowTTL,omitempty"`
	FailedWorkflowTTL    *metav1.Duration `json:"failedWorkflowTTL,omitempty"`
}

type JobTemplateSpec struct {
	Image         string              `json:"image"`
	Command       []string            `json:"command,omitempty"`
	Args          []string            `json:"args,omitempty"`
	CredentialRef NamespacedReference `json:"credentialRef,omitempty"`
}

type ContractReference struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ArtifactReference struct {
	Name   string `json:"name"`
	Digest string `json:"digest,omitempty"`
}

// TypedLocalReference identifies the domain primitive owned by a StepAttempt.
type TypedLocalReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=attempt
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="step attempt spec is immutable"
type StepAttempt struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StepAttemptSpec   `json:"spec"`
	Status StepAttemptStatus `json:"status,omitempty"`
}

// StepAttemptSpec is a workflow lifecycle envelope. Domain execution intent is
// held by the owned AgentRun, UtilityOperation, ApprovalRequest, or ValidationRun.
type StepAttemptSpec struct {
	WorkflowRef UIDReference  `json:"workflowRef"`
	StepName    string        `json:"stepName"`
	Attempt     int32         `json:"attempt"`
	Kind        ExecutionKind `json:"kind"`
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
	ObservedGeneration int64                `json:"observedGeneration,omitempty"`
	Phase              ResourcePhase        `json:"phase,omitempty"`
	ExecutionRef       *TypedLocalReference `json:"executionRef,omitempty"`
	FailureReason      string               `json:"failureReason,omitempty"`
	Retryable          bool                 `json:"retryable,omitempty"`
	StartedAt          *metav1.Time         `json:"startedAt,omitempty"`
	CompletedAt        *metav1.Time         `json:"completedAt,omitempty"`
	Conditions         []metav1.Condition   `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type StepAttemptList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StepAttempt `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=arun
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="agent run spec is immutable"
type AgentRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentRunSpec   `json:"spec"`
	Status AgentRunStatus `json:"status,omitempty"`
}

type AgentRunSpec struct {
	AttemptRef      string                `json:"attemptRef"`
	WorkflowRef     UIDReference          `json:"workflowRef"`
	StepName        string                `json:"stepName"`
	Attempt         int32                 `json:"attempt"`
	Responsibility  string                `json:"responsibility"`
	Image           string                `json:"image"`
	Executable      []string              `json:"executable"`
	Capabilities    []string              `json:"capabilities,omitempty"`
	Inputs          []ArtifactReference   `json:"inputs,omitempty"`
	OutputContracts []ContractReference   `json:"outputContracts,omitempty"`
	Inference       *InferenceRequestSpec `json:"inference,omitempty"`
	Timeout         *metav1.Duration      `json:"timeout,omitempty"`
}

type AgentRunStatus struct {
	ObservedGeneration      int64              `json:"observedGeneration,omitempty"`
	Phase                   ResourcePhase      `json:"phase,omitempty"`
	PodRef                  string             `json:"podRef,omitempty"`
	CollectorJobRef         string             `json:"collectorJobRef,omitempty"`
	InferenceLeaseRef       string             `json:"inferenceLeaseRef,omitempty"`
	WorkspaceWriterLeaseRef string             `json:"workspaceWriterLeaseRef,omitempty"`
	WorkspaceWriterEpoch    int32              `json:"workspaceWriterEpoch,omitempty"`
	WorkspaceWriterReleased bool               `json:"workspaceWriterReleased,omitempty"`
	FailureReason           string             `json:"failureReason,omitempty"`
	Retryable               bool               `json:"retryable,omitempty"`
	StartedAt               *metav1.Time       `json:"startedAt,omitempty"`
	CompletedAt             *metav1.Time       `json:"completedAt,omitempty"`
	Conditions              []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRun `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uop
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="utility operation spec is immutable"
type UtilityOperation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UtilityOperationSpec   `json:"spec"`
	Status UtilityOperationStatus `json:"status,omitempty"`
}

type UtilityOperationSpec struct {
	AttemptRef      string                  `json:"attemptRef"`
	WorkflowRef     UIDReference            `json:"workflowRef"`
	StepName        string                  `json:"stepName"`
	Attempt         int32                   `json:"attempt"`
	Operation       UtilityOperationRequest `json:"operation"`
	Inputs          []ArtifactReference     `json:"inputs,omitempty"`
	OutputContracts []ContractReference     `json:"outputContracts,omitempty"`
	Timeout         *metav1.Duration        `json:"timeout,omitempty"`
}

type UtilityOperationStatus struct {
	ObservedGeneration      int64              `json:"observedGeneration,omitempty"`
	Phase                   ResourcePhase      `json:"phase,omitempty"`
	JobRef                  string             `json:"jobRef,omitempty"`
	CollectorJobRef         string             `json:"collectorJobRef,omitempty"`
	PolicyDecisionID        string             `json:"policyDecisionID,omitempty"`
	WorkspaceWriterLeaseRef string             `json:"workspaceWriterLeaseRef,omitempty"`
	WorkspaceWriterEpoch    int32              `json:"workspaceWriterEpoch,omitempty"`
	WorkspaceWriterReleased bool               `json:"workspaceWriterReleased,omitempty"`
	FailureReason           string             `json:"failureReason,omitempty"`
	Retryable               bool               `json:"retryable,omitempty"`
	StartedAt               *metav1.Time       `json:"startedAt,omitempty"`
	CompletedAt             *metav1.Time       `json:"completedAt,omitempty"`
	Conditions              []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type UtilityOperationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UtilityOperation `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=areq
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="approval request spec is immutable"
type ApprovalRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ApprovalRequestSpec   `json:"spec"`
	Status ApprovalRequestStatus `json:"status,omitempty"`
}

type ApprovalRequestSpec struct {
	AttemptRef  UIDReference        `json:"attemptRef"`
	WorkflowRef UIDReference        `json:"workflowRef"`
	StepName    string              `json:"stepName"`
	Attempt     int32               `json:"attempt"`
	Approval    ApprovalSpec        `json:"approval"`
	Inputs      []ArtifactReference `json:"inputs,omitempty"`
}

type ApprovalRequestStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ResourcePhase      `json:"phase,omitempty"`
	DecisionRef        string             `json:"decisionRef,omitempty"`
	FailureReason      string             `json:"failureReason,omitempty"`
	Retryable          bool               `json:"retryable,omitempty"`
	StartedAt          *metav1.Time       `json:"startedAt,omitempty"`
	CompletedAt        *metav1.Time       `json:"completedAt,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type ApprovalRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApprovalRequest `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="approval decision spec is immutable"
type ApprovalDecision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ApprovalDecisionSpec   `json:"spec"`
	Status ApprovalDecisionStatus `json:"status,omitempty"`
}

type ApprovalDecisionSpec struct {
	WorkflowRef        UIDReference   `json:"workflowRef"`
	StepAttemptRef     UIDReference   `json:"stepAttemptRef"`
	ApprovalRequestRef UIDReference   `json:"approvalRequestRef"`
	Decision           ApprovalChoice `json:"decision"`
	Reason             string         `json:"reason"`
	AuthoredAt         *metav1.Time   `json:"authoredAt"`
	Subject            Subject        `json:"subject"`
}

type ApprovalDecisionStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type ApprovalDecisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApprovalDecision `json:"items"`
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
	WorkflowRef      UIDReference        `json:"workflowRef"`
	ProducerRef      TypedLocalReference `json:"producerRef"`
	ProducerUID      types.UID           `json:"producerUID"`
	ProducerGrantRef UIDReference        `json:"producerGrantRef"`
	Contract         ContractReference   `json:"contract"`
	Digest           string              `json:"digest"`
	Path             string              `json:"path"`
	Classification   string              `json:"classification,omitempty"`
	SourceRevision   string              `json:"sourceRevision,omitempty"`
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
	WorkflowRef      UIDReference     `json:"workflowRef"`
	RequesterSubject string           `json:"requesterSubject"`
	ToolProfile      string           `json:"toolProfile"`
	Capabilities     []string         `json:"capabilities,omitempty"`
	TTL              *metav1.Duration `json:"ttl"`
}

type HumanSessionStatus struct {
	ObservedGeneration      int64              `json:"observedGeneration,omitempty"`
	Phase                   ResourcePhase      `json:"phase,omitempty"`
	PodRef                  string             `json:"podRef,omitempty"`
	ServiceRef              string             `json:"serviceRef,omitempty"`
	AccessURL               string             `json:"accessURL,omitempty"`
	BeforeRevision          string             `json:"beforeRevision,omitempty"`
	AfterRevision           string             `json:"afterRevision,omitempty"`
	WorkspaceWriterLeaseRef string             `json:"workspaceWriterLeaseRef,omitempty"`
	WorkspaceWriterEpoch    int32              `json:"workspaceWriterEpoch,omitempty"`
	WorkspaceWriterReleased bool               `json:"workspaceWriterReleased,omitempty"`
	ExpiresAt               *metav1.Time       `json:"expiresAt,omitempty"`
	Conditions              []metav1.Condition `json:"conditions,omitempty"`
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
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="validation run spec is immutable"
type ValidationRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ValidationRunSpec   `json:"spec"`
	Status ValidationRunStatus `json:"status,omitempty"`
}

type ValidationRunSpec struct {
	AttemptRef  string              `json:"attemptRef"`
	WorkflowRef UIDReference        `json:"workflowRef"`
	StepName    string              `json:"stepName"`
	Attempt     int32               `json:"attempt"`
	Provider    string              `json:"provider"`
	Commit      string              `json:"commit,omitempty"`
	ImageDigest string              `json:"imageDigest,omitempty"`
	OverlayPath string              `json:"overlayPath,omitempty"`
	Destination string              `json:"destination,omitempty"`
	Inputs      []ArtifactReference `json:"inputs,omitempty"`
}

type ValidationRunStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ResourcePhase      `json:"phase,omitempty"`
	ProviderRef        string             `json:"providerRef,omitempty"`
	AccessURL          string             `json:"accessURL,omitempty"`
	ResultArtifactRef  string             `json:"resultArtifactRef,omitempty"`
	FailureReason      string             `json:"failureReason,omitempty"`
	Retryable          bool               `json:"retryable,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type ValidationRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ValidationRun `json:"items"`
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
