package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
)

// ApprovalDecisionEvidence survives deletion of workflow Kubernetes resources.
// ReviewedInputs are the exact immutable pins shown by the approval request.
type ApprovalDecisionEvidence struct {
	SchemaVersion   string                       `json:"schemaVersion"`
	Workflow        v1alpha1.UIDReference        `json:"workflow"`
	StepAttempt     v1alpha1.UIDReference        `json:"stepAttempt"`
	Request         v1alpha1.UIDReference        `json:"request"`
	Decision        v1alpha1.UIDReference        `json:"decision"`
	Approver        v1alpha1.Subject             `json:"approver"`
	Choice          v1alpha1.ApprovalChoice      `json:"choice"`
	Reason          string                       `json:"reason,omitempty"`
	ApprovalPolicy  v1alpha1.ApprovalSpec        `json:"approvalPolicy"`
	ReviewedInputs  []v1alpha1.ArtifactReference `json:"reviewedInputs"`
	AuthoredAt      time.Time                    `json:"authoredAt"`
	AdmittedAt      *time.Time                   `json:"admittedAt,omitempty"`
	SubmissionEvent string                       `json:"submissionEvent,omitempty"`
	InputEvent      string                       `json:"inputEvent"`
}

func ApprovalSubmissionEvent(request *v1alpha1.ApprovalRequest, decision *v1alpha1.ApprovalDecision) (Event, error) {
	if request.UID == "" || decision.UID == "" {
		return Event{}, fmt.Errorf("approval evidence requires persisted request and decision UIDs")
	}
	if decision.Spec.AuthoredAt == nil {
		return Event{}, fmt.Errorf("approval decision has no authored time")
	}
	payload := ApprovalDecisionEvidence{
		SchemaVersion: PayloadSchemaVersionV1, Workflow: decision.Spec.WorkflowRef, StepAttempt: decision.Spec.StepAttemptRef,
		Request: decision.Spec.ApprovalRequestRef, Decision: v1alpha1.UIDReference{Name: decision.Name, UID: decision.UID},
		Approver: decision.Spec.Subject, Choice: decision.Spec.Decision, Reason: decision.Spec.Reason,
		ApprovalPolicy: request.Spec.Approval, ReviewedInputs: request.Spec.Inputs, AuthoredAt: decision.Spec.AuthoredAt.Time,
	}
	subject := Subject{Namespace: request.Namespace, Workflow: request.Spec.WorkflowRef.Name, Step: request.Spec.StepName, Attempt: request.Spec.Attempt}
	inputEvent, err := NewEvent(EventOptions{Source: "approvalrequest-controller", Type: "InputsResolved", Subject: subject, Action: "resolve-inputs", Target: request.Name, Outcome: "resolved"})
	if err != nil {
		return Event{}, err
	}
	payload.InputEvent = inputEvent.ID
	event, err := NewEvent(EventOptions{Source: "api-server", Type: "ApprovalDecisionSubmitted", Actor: Actor{Kind: "API", ID: "api-server"},
		Requester: &Actor{Kind: "User", ID: decision.Spec.Subject.SubjectId}, Subject: subject,
		OccurredAt: payload.AuthoredAt, Action: "submit", Target: decision.Name, Outcome: "submitted", DecisionID: string(decision.UID),
		References: map[string]string{"approvalRequest": request.Name, "approvalDecision": decision.Name}, Data: payload})
	event.ID = DeterministicID("ApprovalDecisionSubmitted", string(request.UID), string(decision.UID))
	return event, err
}

func ApprovalAdmissionEventID(request *v1alpha1.ApprovalRequest) string {
	return DeterministicID("ApprovalDecisionAdmitted", string(request.UID), string(request.Status.DecisionUID))
}

// RecordApprovalSubmission can be replayed by the controller if API persistence
// succeeded but recording the response/event failed. Identity and time are stored.
func RecordApprovalSubmission(ctx context.Context, recorder Recorder, request *v1alpha1.ApprovalRequest, decision *v1alpha1.ApprovalDecision) error {
	if recorder == nil {
		return nil
	}
	event, err := ApprovalSubmissionEvent(request, decision)
	if err != nil {
		return err
	}
	return recorder.Append(ctx, event)
}

func RecordApprovalAdmission(ctx context.Context, recorder Recorder, request *v1alpha1.ApprovalRequest, decision *v1alpha1.ApprovalDecision) error {
	if request.Status.DecisionRef != decision.Name || request.Status.DecisionUID != decision.UID || request.Status.CompletedAt == nil ||
		(request.Status.Phase != v1alpha1.PhaseSucceeded && request.Status.FailureReason != "ApprovalDenied") {
		return fmt.Errorf("approval admission has no matching persisted completion")
	}
	submission, err := ApprovalSubmissionEvent(request, decision)
	if err != nil {
		return err
	}
	if recorder != nil {
		if err := recorder.Append(ctx, submission); err != nil {
			return err
		}
	}
	// Reuse the same immutable evidence, adding the persisted admission outcome.
	var payload ApprovalDecisionEvidence
	if err := json.Unmarshal(submission.Data, &payload); err != nil {
		return err
	}
	admittedAt := request.Status.CompletedAt.Time
	payload.AdmittedAt = &admittedAt
	payload.SubmissionEvent = submission.ID
	event, err := NewEvent(EventOptions{Source: "approvalrequest-controller", Type: "ApprovalDecisionAdmitted",
		Actor: Actor{Kind: "Controller", ID: "approvalrequest-controller"}, Requester: submission.Requester,
		Subject: submission.Subject, OccurredAt: admittedAt, Action: "admit", Target: decision.Name, Outcome: "admitted",
		DecisionID: string(decision.UID), CausationID: submission.ID, References: submission.References, Data: payload})
	if err != nil {
		return err
	}
	event.ID = ApprovalAdmissionEventID(request)
	if recorder == nil {
		return nil
	}
	return recorder.Append(ctx, event)
}
