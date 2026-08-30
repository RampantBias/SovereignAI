package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type ProjectValidationSpec struct {
	Provider           string `json:"provider"`
	InfrastructureRepo string `json:"infrastructureRepository"`
	OverlayPath        string `json:"overlayPath"`
	ImageName          string `json:"imageName,omitempty"`
}

// StepConfig declares one workflow stage and exactly one domain execution primitive.
// +kubebuilder:validation:XValidation:rule="self.kind != 'Agent' || has(self.agent)",message="agent is required for Agent steps"
// +kubebuilder:validation:XValidation:rule="self.kind == 'Agent' || !has(self.agent)",message="agent is only allowed for Agent steps"
// +kubebuilder:validation:XValidation:rule="self.kind != 'HumanGate' || has(self.approval)",message="approval is required for HumanGate steps"
// +kubebuilder:validation:XValidation:rule="self.kind == 'HumanGate' || !has(self.approval)",message="approval is only allowed for HumanGate steps"
// +kubebuilder:validation:XValidation:rule="self.kind != 'Utility' || has(self.utility)",message="utility is required for Utility steps"
// +kubebuilder:validation:XValidation:rule="self.kind == 'Utility' || !has(self.utility)",message="utility is only allowed for Utility steps"
// +kubebuilder:validation:XValidation:rule="self.kind != 'Validation' || has(self.validation)",message="validation is required for Validation steps"
// +kubebuilder:validation:XValidation:rule="self.kind == 'Validation' || !has(self.validation)",message="validation is only allowed for Validation steps"
type StepConfig struct {
	Name        string                   `json:"name"`
	Kind        ExecutionKind            `json:"kind"`
	Agent       *AgentStepSpec           `json:"agent,omitempty"`
	Utility     *UtilityOperationRequest `json:"utility,omitempty"`
	Approval    *ApprovalSpec            `json:"approval,omitempty"`
	Validation  *ValidationStepSpec      `json:"validation,omitempty"`
	Inputs      []ArtifactReference      `json:"inputs,omitempty"`
	Outputs     []ContractReference      `json:"outputs,omitempty"`
	Timeout     *metav1.Duration         `json:"timeout,omitempty"`
	MaxAttempts int32                    `json:"maxAttempts,omitempty"`
	Order       int                      `json:"order"`
}

// AgentStepSpec declares delegated autonomous work. These fields are never
// copied into deterministic utility, approval, or validation primitives.
type AgentStepSpec struct {
	Responsibility string                `json:"responsibility"`
	Image          string                `json:"image"`
	Executable     []string              `json:"executable"`
	Capabilities   []string              `json:"capabilities,omitempty"`
	Inference      *InferenceRequestSpec `json:"inference,omitempty"`
	Deterministic  bool                  `json:"deterministic,omitempty"`
}

// UtilityOperationRequest declares one platform-owned deterministic operation.
// Parameters are interpreted by the named allow-listed operation; they are not
// an arbitrary process command. The controller derives the idempotency key and
// resolves Project-owned commands and credentials before scheduling a Job.
type UtilityOperationRequest struct {
	// +kubebuilder:validation:Enum=repository.initialize;git.createBranch;candidate.prepare;git.commit;git.push;git.merge;test.run;build.image
	Name       string            `json:"name"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

type ApprovalSpec struct {
	Mode           ApprovalMode `json:"mode"`
	RequiredGroups []string     `json:"requiredGroups"`
	DenyBehavior   string       `json:"denyBehavior"`
}

// ValidationStepSpec declares the subject and provider contract for a
// validation authority. Empty provider-specific values are resolved from the
// owning Project by the ValidationRun controller.
type ValidationStepSpec struct {
	Provider    string `json:"provider"`
	Commit      string `json:"commit,omitempty"`
	ImageDigest string `json:"imageDigest,omitempty"`
	OverlayPath string `json:"overlayPath,omitempty"`
	Destination string `json:"destination,omitempty"`
}

// WorkflowBootstrapSpec identifies the immutable ingress object whose exact
// bytes must become the first accepted workflow Artifact.
type WorkflowBootstrapSpec struct {
	SourceRef      UIDReference      `json:"sourceRef"`
	Key            string            `json:"key"`
	ExpectedDigest string            `json:"expectedDigest"`
	Contract       ContractReference `json:"contract"`
	ArtifactName   string            `json:"artifactName"`
}

// SovereignWorkflowSpec defines the desired state (The user's intent)
type SovereignWorkflowSpec struct {
	Project    UIDReference `json:"projectRef"`
	WorkflowID string       `json:"workflowId"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="requesterSubject is immutable"
	RequesterSubject string `json:"requesterSubject"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="bootstrap is immutable"
	Bootstrap           WorkflowBootstrapSpec `json:"bootstrap"`
	DefinitionRevision  string                `json:"definitionRevision,omitempty"`
	Classification      string                `json:"classification,omitempty"`
	Steps               []StepConfig          `json:"steps"`
	RequestedVolumeSize string                `json:"requestedVolumeSize"`
	// MaxWorkflowAttempt bounds application-level test failure rewinds independently of infrastructure retries.
	// +kubebuilder:validation:Minimum=1
	MaxWorkflowAttempt int32 `json:"maxWorkflowAttempt,omitempty"`
}

// SelectedArtifact records the exact accepted artifact identity used for
// subsequent workflow inputs. ProducerAttemptRef is empty for bootstrap
// artifacts produced directly by the workflow.
type SelectedArtifact struct {
	Contract           ContractReference `json:"contract"`
	Digest             string            `json:"digest"`
	ProducerAttemptRef string            `json:"producerAttemptRef,omitempty"`
}

// WorkflowRefinementStatus records an application-level test failure that
// rewound the workflow. It is distinct from infrastructure retry state.
type WorkflowRefinementStatus struct {
	Iteration         int32                       `json:"iteration"`
	TriggerStepName   string                      `json:"triggerStep"`
	TriggerAttemptRef string                      `json:"triggerAttemptRef"`
	RestartStepName   string                      `json:"restartStep"`
	TestReport        *SelectedArtifact           `json:"testReport,omitempty"`
	AgentCompletions  []RefinementAgentCompletion `json:"agentCompletions,omitempty"`
}

type RefinementAgentCompletion struct {
	StepName   string                `json:"stepName"`
	AttemptRef string                `json:"attemptRef"`
	Completion AgentCompletionStatus `json:"completion"`
}

// SovereignWorkflowStatus defines the observed state
type SovereignWorkflowStatus struct {
	Phase                   string                    `json:"phase"`                      // e.g., Pending, Running, Stalled, Completed
	ActiveStepName          string                    `json:"activeStep,omitempty"`       // Currently executing step
	ActiveAttemptRef        string                    `json:"activeAttemptRef,omitempty"` // Reference to the current attempt
	WorkflowAttempt         int32                     `json:"workflowAttempt"`
	ObservedGeneration      int64                     `json:"observedGeneration,omitempty"`
	PvcName                 string                    `json:"pvcName,omitempty"`                 // Bound storage resource
	WorkspaceWriterLeaseRef string                    `json:"workspaceWriterLeaseRef,omitempty"` // Lease serializing writable workspace mounts
	BootstrapJobRef         string                    `json:"bootstrapJobRef,omitempty"`
	BootstrapArtifactRef    *UIDReference             `json:"bootstrapArtifactRef,omitempty"`
	BootstrapWriterEpoch    int32                     `json:"bootstrapWriterEpoch,omitempty"`
	BootstrapWriterReleased bool                      `json:"bootstrapWriterReleased,omitempty"`
	SelectedArtifacts       []SelectedArtifact        `json:"selectedArtifacts,omitempty"`
	Refinement              *WorkflowRefinementStatus `json:"refinement,omitempty"`
	Conditions              []metav1.Condition        `json:"conditions,omitempty"` // Standard K8s status conditions
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
	// ValidationProviderRef is the provisioned Argo AppProject name.
	ValidationProviderRef string             `json:"validationProviderRef,omitempty"`
	ObservedGeneration    int64              `json:"observedGeneration,omitempty"`
	Phase                 ResourcePhase      `json:"phase,omitempty"`
	Conditions            []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type SovereignProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SovereignProject `json:"items"`
}
