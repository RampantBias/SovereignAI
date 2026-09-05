package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// WorkflowRecoveryEvidence freezes the decision's inputs before the workflow
// advances, allowing audit writes to be retried without inventing new evidence.
type WorkflowRecoveryEvidence struct {
	SelectedAt          metav1.Time         `json:"selectedAt"`
	TriggerAttempt      UIDReference        `json:"triggerAttempt"`
	PreviousAttempt     *UIDReference       `json:"previousAttempt,omitempty"`
	PreviousOutcome     ResourcePhase       `json:"previousOutcome,omitempty"`
	FromWorkflowAttempt int32               `json:"fromWorkflowAttempt"`
	MaxWorkflowAttempt  int32               `json:"maxWorkflowAttempt"`
	FailureReason       string              `json:"failureReason"`
	Feedback            *FailedAgentAttempt `json:"feedback,omitempty"`
	InputLinks          []RecoveryInputLink `json:"inputLinks,omitempty"`
	Reports             []ArtifactReference `json:"reports,omitempty"`
	EvidenceEvents      []string            `json:"evidenceEvents,omitempty"`
	Missing             []string            `json:"missing,omitempty"`
}

// RecoveryInputLink proves a consumed artifact's producing attempt. All refs
// name immutable objects; these are not timestamp-based ancestry guesses.
type RecoveryInputLink struct {
	Consumer UIDReference      `json:"consumer"`
	Input    ArtifactReference `json:"input"`
	Producer UIDReference      `json:"producer"`
}
