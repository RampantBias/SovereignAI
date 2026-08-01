package v1alpha1

import "k8s.io/apimachinery/pkg/types"

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

type UIDReference struct {
	Name string    `json:"name"`
	UID  types.UID `json:"uid"`
}

type NamespacedReference struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}
