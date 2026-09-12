package audit

import (
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
)

// WorkflowRecoveryDecision extends the ordinary decision shape with the frozen
// cross-step rewind evidence. NextAttemptName is planned, not proof of creation.
type WorkflowRecoveryDecision struct {
	DecisionEvaluated
	Action            string                            `json:"action"`
	RestartStep       string                            `json:"restartStep"`
	ToWorkflowAttempt int32                             `json:"toWorkflowAttempt"`
	NextAttemptName   string                            `json:"nextAttemptName"`
	Recovery          v1alpha1.WorkflowRecoveryEvidence `json:"recovery"`
}

// WorkflowRetryRecorded is emitted only after the replacement attempt and its
// AgentRun exist. Feedback and inputs come from that stored execution spec.
type WorkflowRetryRecorded struct {
	// ObservedAt is persisted before audit append; absent on older events.
	ObservedAt      *time.Time                   `json:"observedAt,omitempty"`
	SchemaVersion   string                       `json:"schemaVersion"`
	Workflow        ResourceRef                  `json:"workflow"`
	DecisionEvent   string                       `json:"decisionEvent"`
	RetryOf         *v1alpha1.UIDReference       `json:"retryOf,omitempty"`
	TriggeredBy     v1alpha1.UIDReference        `json:"triggeredBy"`
	Attempt         ResourceRef                  `json:"attempt"`
	Execution       ResourceRef                  `json:"execution"`
	WorkflowAttempt int32                        `json:"workflowAttempt"`
	Feedback        *v1alpha1.FailedAgentAttempt `json:"feedback,omitempty"`
	Inputs          []v1alpha1.ArtifactReference `json:"inputs"`
}
