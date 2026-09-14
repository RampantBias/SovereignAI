package v1

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SovereignAI/internal/api/requestidentity"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/domain/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SubmitApproval records human intent. Controllers own request and attempt status.
// The show action only inspects the current request and never creates a decision.
func (s *Server) SubmitApproval(ctx context.Context, req *pb.ApprovalSubmission) (*pb.ApprovalResponse, error) {
	// cli is already authenticated, this is used for group validation mainly
	identity, ok := requestidentity.FromContext(ctx)
	if !ok || !identity.Valid() {
		return nil, status.Error(codes.Unauthenticated, "authenticated approver is required")
	}

	// simple rule violation, but not existence
	action := strings.ToLower(strings.TrimSpace(req.GetAction()))
	workflowID := strings.TrimSpace(req.GetWorkflowId())
	requestUID := strings.TrimSpace(req.GetRequestUid())
	if err := validateArguments(action, workflowID, requestUID); err != nil {
		return nil, err
	}

	// check for already active approval
	approval, err := s.activeApproval(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	if requestUID != "" && requestUID != string(approval.UID) {
		return nil, status.Error(codes.FailedPrecondition, "request UID does not match the active approval request; inspect it again")
	}

	// authorization is membership foucsed
	if authorized := isAuthorized(identity, approval); !authorized {
		return nil, status.Error(codes.PermissionDenied, "approver must belong to a required approval group")
	}

	if action == "show" {
		message := fmt.Sprintf("Workflow: %s\nRequest: %s\nRequest UID: %s\nPhase: %s\nRequired groups: %s\nMode: %s",
			workflowID, approval.Name, approval.UID, approval.Status.Phase,
			strings.Join(approval.Spec.Approval.RequiredGroups, ", "), approval.Spec.Approval.Mode)
		for _, input := range approval.Spec.Inputs {
			message += fmt.Sprintf("\nInput: %s (%s)", input.Name, input.Digest)
		}
		if approval.Status.DecisionRef != "" {
			message += "\nDecision: " + approval.Status.DecisionRef
		}
		return &pb.ApprovalResponse{Success: true, Message: message}, nil
	}

	if approval.Spec.Approval.Mode != v1alpha1.AnyOf || approval.Spec.Approval.DenyBehavior != "Fail" {
		return nil, status.Error(codes.FailedPrecondition, "only AnyOf approval with Fail denial behavior is supported")
	}

	choice := v1alpha1.Approved
	if action == "deny" {
		choice = v1alpha1.Denied
	}
	decision := buildApprovalDecision(identity, choice, approval)

	var existing v1alpha1.ApprovalDecision
	if err := s.Client.Get(ctx, client.ObjectKeyFromObject(decision), &existing); err == nil {
		return s.recordApprovalSubmission(ctx, approval, &existing, decision, workflowID)
	} else if !apierrors.IsNotFound(err) {
		return nil, status.Errorf(codes.Internal, "failed to read approval decision: %v", err)
	}
	if approval.Status.Phase != v1alpha1.PhaseAwaitingApproval {
		return nil, status.Error(codes.FailedPrecondition, "request is not awaiting approval")
	}
	if err := s.Client.Create(ctx, decision); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, status.Errorf(codes.Internal, "failed to create approval decision: %v", err)
		}
		if err := s.Client.Get(ctx, client.ObjectKeyFromObject(decision), &existing); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to read existing approval decision: %v", err)
		}
		return s.recordApprovalSubmission(ctx, approval, &existing, decision, workflowID)
	}
	if err := audit.RecordApprovalSubmission(ctx, s.Auditor, approval, decision); err != nil {
		return nil, status.Errorf(codes.Internal, "decision persisted but audit submission failed: %v", err)
	}
	return &pb.ApprovalResponse{Success: true, Message: fmt.Sprintf("%s decision %s submitted for %s; awaiting controller processing", choice, decision.Name, workflowID)}, nil
}

func validateArguments(action, workflowID, requestUID string) error {
	if workflowID == "" {
		return status.Error(codes.InvalidArgument, "workflow-id is required")
	}
	if action != "show" && action != "approve" && action != "deny" {
		return status.Error(codes.InvalidArgument, "action must be show, approve, or deny")
	}
	if action != "show" && requestUID == "" {
		return status.Error(codes.InvalidArgument, "request-uid is required for approve and deny")
	}
	return nil
}

func isAuthorized(identity requestidentity.Identity, approval *v1alpha1.ApprovalRequest) bool {
	authorized := false
	for _, group := range approval.Spec.Approval.RequiredGroups {
		if group != "" && identity.InGroup(group) {
			authorized = true
			break
		}
	}
	return authorized
}

func approvalSubmissionResult(existing, requested *v1alpha1.ApprovalDecision, workflowID string) (*pb.ApprovalResponse, error) {
	if !existing.DeletionTimestamp.IsZero() ||
		existing.Spec.WorkflowRef != requested.Spec.WorkflowRef ||
		existing.Spec.StepAttemptRef != requested.Spec.StepAttemptRef ||
		existing.Spec.ApprovalRequestRef != requested.Spec.ApprovalRequestRef ||
		existing.Spec.Subject.SubjectId != requested.Spec.Subject.SubjectId ||
		existing.Spec.Decision != requested.Spec.Decision {
		return nil, status.Error(codes.AlreadyExists, "a different decision already exists for this approval request")
	}
	return &pb.ApprovalResponse{Success: true, Message: fmt.Sprintf("%s decision %s already submitted for %s", existing.Spec.Decision, existing.Name, workflowID)}, nil
}

func buildApprovalDecision(identity requestidentity.Identity, choice v1alpha1.ApprovalChoice, approval *v1alpha1.ApprovalRequest) *v1alpha1.ApprovalDecision {
	now := v1alpha1.NewAuditTime(time.Now())
	return &v1alpha1.ApprovalDecision{
		ObjectMeta: metav1.ObjectMeta{
			// One immutable decision per request. Kubernetes create arbitrates concurrent submissions.
			Name:            controllermeta.ApprovalDecisionName(approval.UID),
			Namespace:       approval.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(approval, v1alpha1.GroupVersion.WithKind("ApprovalRequest"))},
		},
		Spec: v1alpha1.ApprovalDecisionSpec{
			WorkflowRef:        approval.Spec.WorkflowRef,
			StepAttemptRef:     approval.Spec.AttemptRef,
			ApprovalRequestRef: v1alpha1.UIDReference{Name: approval.Name, UID: approval.UID},
			Decision:           choice,
			AuthoredAt:         &now,
			Subject:            v1alpha1.Subject{SubjectId: identity.Subject, Groups: append([]string(nil), identity.Groups...)},
		},
	}
}

func (s *Server) activeApproval(ctx context.Context, workflowID string) (*v1alpha1.ApprovalRequest, error) {
	var workflows v1alpha1.SovereignWorkflowList
	if err := s.Client.List(ctx, &workflows, client.MatchingLabels{"sovereign-ai.io/workflow-id": workflowID}); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to locate workflow: %v", err)
	}
	if len(workflows.Items) == 0 {
		return nil, status.Error(codes.NotFound, "workflow was not found")
	}
	if len(workflows.Items) != 1 {
		return nil, status.Error(codes.FailedPrecondition, "workflow ID is ambiguous")
	}
	workflow := &workflows.Items[0]
	if workflow.UID == "" || workflow.Spec.WorkflowID != workflowID || !workflow.DeletionTimestamp.IsZero() || state.IsTerminal(v1alpha1.ResourcePhase(workflow.Status.Phase)) || workflow.Status.ActiveAttemptRef == "" {
		return nil, status.Error(codes.FailedPrecondition, "workflow has no active approval request")
	}
	var attempt v1alpha1.StepAttempt
	if err := s.Client.Get(ctx, types.NamespacedName{Namespace: workflow.Namespace, Name: workflow.Status.ActiveAttemptRef}, &attempt); err != nil {
		return nil, approvalLookupError("step attempt", err)
	}
	workflowRef := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}
	execution := attempt.Status.ExecutionRef
	if attempt.UID == "" || !attempt.DeletionTimestamp.IsZero() || state.IsTerminal(attempt.Status.Phase) ||
		!metav1.IsControlledBy(&attempt, workflow) || attempt.Spec.WorkflowRef != workflowRef ||
		attempt.Spec.StepName != workflow.Status.ActiveStepName || attempt.Spec.Kind != v1alpha1.ExecutionKindHumanGate ||
		execution == nil || execution.APIVersion != v1alpha1.GroupVersion.String() || execution.Kind != "ApprovalRequest" || execution.Name == "" {
		return nil, status.Error(codes.FailedPrecondition, "active attempt is not an authorized human gate")
	}
	var approval v1alpha1.ApprovalRequest
	if err := s.Client.Get(ctx, types.NamespacedName{Namespace: workflow.Namespace, Name: execution.Name}, &approval); err != nil {
		return nil, approvalLookupError("approval request", err)
	}
	if approval.UID == "" || !approval.DeletionTimestamp.IsZero() || !metav1.IsControlledBy(&approval, &attempt) ||
		approval.Spec.WorkflowRef != workflowRef || approval.Spec.AttemptRef != (v1alpha1.UIDReference{Name: attempt.Name, UID: attempt.UID}) ||
		approval.Spec.StepName != attempt.Spec.StepName || approval.Spec.Attempt != attempt.Spec.RetryNumber {
		return nil, status.Error(codes.FailedPrecondition, "approval request does not match the active attempt")
	}
	return &approval, nil
}

func approvalLookupError(resource string, err error) error {
	if apierrors.IsNotFound(err) {
		return status.Errorf(codes.FailedPrecondition, "active %s was not found", resource)
	}
	return status.Errorf(codes.Internal, "failed to read %s: %v", resource, err)
}

func (s *Server) recordApprovalSubmission(ctx context.Context, approval *v1alpha1.ApprovalRequest, existing, requested *v1alpha1.ApprovalDecision, workflowID string) (*pb.ApprovalResponse, error) {
	response, err := approvalSubmissionResult(existing, requested, workflowID)
	if err != nil {
		return nil, err
	}
	if err := audit.RecordApprovalSubmission(ctx, s.Auditor, approval, existing); err != nil {
		return nil, status.Errorf(codes.Internal, "decision persisted but audit submission failed: %v", err)
	}
	return response, nil
}
