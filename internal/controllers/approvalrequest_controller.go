package controllers

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/domain/state"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ApprovalRequestReconciler owns the request side of human authority.
// It consumes immutable decisions and leaves attempt/workflow status to their controllers.
type ApprovalRequestReconciler struct {
	client.Client
	Audit audit.Recorder
	Now   func() time.Time
}

func (r *ApprovalRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ApprovalRequest{}).
		Owns(&v1alpha1.ApprovalDecision{}).
		Complete(r)
}

func (r *ApprovalRequestReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var approval v1alpha1.ApprovalRequest
	if err := r.Get(ctx, request.NamespacedName, &approval); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !approval.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	terminating, err := NamespaceTerminating(ctx, r.Client, approval.Namespace)
	if err != nil || terminating {
		return ctrl.Result{}, err
	}
	if state.IsTerminal(approval.Status.Phase) {
		return ctrl.Result{}, r.recordAdmittedDecision(ctx, &approval)
	}
	if approval.Status.Phase == v1alpha1.PhaseAwaitingApproval {
		return ctrl.Result{}, r.resolveDecision(ctx, &approval)
	}
	attempt, invalidReason, err := r.approvalAttempt(ctx, &approval)
	if err != nil {
		return ctrl.Result{}, err
	}
	if invalidReason != "" {
		return ctrl.Result{}, r.setFailed(ctx, &approval, "InvalidStepAttemptAuthority")
	}
	if attempt == nil {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	evidence, ready, invalidReason, err := ResolveArtifactEvidence(
		ctx,
		r.Client,
		approval.Namespace,
		approval.Spec.WorkflowRef,
		approval.Spec.Inputs,
	)
	if err != nil {
		return ctrl.Result{}, err
	}
	if invalidReason != "" {
		return ctrl.Result{}, r.setFailed(ctx, &approval, "InvalidArtifactInputs")
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.appendInputsResolved(ctx, &approval, evidence); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.setAwaiting(ctx, &approval); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, audit.AppendControllerEvent(ctx, r.Audit, "approvalrequest-controller", r.Now, audit.EventOptions{
		Type: "ApprovalRequested", Subject: audit.Subject{Namespace: approval.Namespace, Workflow: approval.Spec.WorkflowRef.Name, Step: approval.Spec.StepName, Attempt: approval.Spec.Attempt},
		Action: "request", Target: approval.Name, Outcome: "awaiting", References: map[string]string{"approvalRequest": approval.Name, "stepAttempt": approval.Spec.AttemptRef.Name},
		Data: map[string]any{"mode": approval.Spec.Approval.Mode, "requiredGroups": approval.Spec.Approval.RequiredGroups},
	})
}

func (r *ApprovalRequestReconciler) setFailed(ctx context.Context, approval *v1alpha1.ApprovalRequest, reason string) error {
	return r.updateApprovalStatus(ctx, approval, func(latest *v1alpha1.ApprovalRequest) (bool, error) {
		r.finishApproval(latest, v1alpha1.PhaseFailed, reason, "approval request failed authority or input validation", "")
		return true, nil
	})
}
func (r *ApprovalRequestReconciler) appendInputsResolved(ctx context.Context, approval *v1alpha1.ApprovalRequest, evidence []audit.ArtifactEvidence) error {
	payload := InputsResolvedPayload("ApprovalRequest", approval, evidence)
	return audit.AppendControllerEvent(ctx, r.Audit, "approvalrequest-controller", r.Now, audit.EventOptions{
		Type: "InputsResolved",
		Subject: audit.Subject{
			Namespace: approval.Namespace,
			Workflow:  approval.Spec.WorkflowRef.Name,
			Step:      approval.Spec.StepName,
			Attempt:   approval.Spec.Attempt,
		},
		Action:  "resolve-inputs",
		Target:  approval.Name,
		Outcome: "resolved",
		References: map[string]string{
			"approvalRequest": approval.Name,
			"stepAttempt":     approval.Spec.AttemptRef.Name,
		},
		Data: payload,
	})
}

func (r *ApprovalRequestReconciler) setAwaiting(ctx context.Context, approval *v1alpha1.ApprovalRequest) error {
	return r.updateApprovalStatus(ctx, approval, func(latest *v1alpha1.ApprovalRequest) (bool, error) {
		if latest.Status.Phase == v1alpha1.PhaseAwaitingApproval {
			return false, nil
		}
		latest.Status.Phase = v1alpha1.PhaseAwaitingApproval
		if latest.Status.StartedAt == nil {
			now := r.approvalTime()
			latest.Status.StartedAt = &now
		}
		latest.Status.ObservedGeneration = latest.Generation
		apiMeta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: "Ready", Status: metav1.ConditionUnknown, Reason: "AwaitingDecision", Message: "waiting for an authoritative ApprovalDecision", ObservedGeneration: latest.Generation,
		})
		return true, nil
	})
}

// Re-read the request on every retry, and never write a replacement or terminal request.
func (r *ApprovalRequestReconciler) updateApprovalStatus(ctx context.Context, approval *v1alpha1.ApprovalRequest, mutate func(*v1alpha1.ApprovalRequest) (bool, error)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.ApprovalRequest
		if err := r.Get(ctx, client.ObjectKeyFromObject(approval), &latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		if latest.UID != approval.UID || !latest.DeletionTimestamp.IsZero() || state.IsTerminal(latest.Status.Phase) {
			return nil
		}
		changed, err := mutate(&latest)
		if err != nil || !changed {
			return err
		}
		return r.Status().Update(ctx, &latest)
	})
}

func (r *ApprovalRequestReconciler) resolveDecision(ctx context.Context, approval *v1alpha1.ApprovalRequest) error {
	err := r.updateApprovalStatus(ctx, approval, func(latest *v1alpha1.ApprovalRequest) (bool, error) {
		if latest.Status.Phase != v1alpha1.PhaseAwaitingApproval {
			return false, nil
		}
		var decision v1alpha1.ApprovalDecision
		if err := r.Get(ctx, types.NamespacedName{Namespace: latest.Namespace, Name: controllermeta.ApprovalDecisionName(latest.UID)}, &decision); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if message := validateApprovalDecision(latest, &decision); message != "" {
			r.finishApproval(latest, v1alpha1.PhaseFailed, "InvalidApprovalDecision", message, "")
			return true, nil
		}
		// Recheck live authority at consumption time, including on status conflict retries.
		attempt, invalidReason, err := r.approvalAttempt(ctx, latest)
		if err != nil {
			return false, err
		}
		if invalidReason != "" || attempt == nil {
			r.finishApproval(latest, v1alpha1.PhaseFailed, "InvalidStepAttemptAuthority", "approval request no longer has an active authorized attempt", "")
			return true, nil
		}
		var workflow v1alpha1.SovereignWorkflow
		err = r.Get(ctx, types.NamespacedName{Namespace: latest.Namespace, Name: latest.Spec.WorkflowRef.Name}, &workflow)
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		if apierrors.IsNotFound(err) || workflow.UID != latest.Spec.WorkflowRef.UID || !workflow.DeletionTimestamp.IsZero() ||
			state.IsTerminal(v1alpha1.ResourcePhase(workflow.Status.Phase)) || !metav1.IsControlledBy(attempt, &workflow) ||
			workflow.Status.ActiveAttemptRef != attempt.Name || workflow.Status.ActiveStepName != attempt.Spec.StepName ||
			workflow.Status.WorkflowAttempt != attempt.Spec.WorkflowAttempt {
			r.finishApproval(latest, v1alpha1.PhaseFailed, "InvalidWorkflowAuthority", "approval request is not the workflow's active gate", "")
			return true, nil
		}
		_, ready, invalid, err := ResolveArtifactEvidence(ctx, r.Client, latest.Namespace, latest.Spec.WorkflowRef, latest.Spec.Inputs)
		if err != nil {
			return false, err
		}
		if invalid != "" || !ready {
			return false, fmt.Errorf("approval inputs are unavailable or invalid: %s", invalid)
		}
		if err := audit.RecordApprovalSubmission(ctx, r.Audit, latest, &decision); err != nil {
			return false, err
		}
		latest.Status.DecisionUID = decision.UID
		if decision.Spec.Decision == v1alpha1.Denied {
			r.finishApproval(latest, v1alpha1.PhaseFailed, "ApprovalDenied", "human approval was denied", decision.Name)
		} else {
			r.finishApproval(latest, v1alpha1.PhaseSucceeded, "ApprovalGranted", "human approval was granted", decision.Name)
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	var completed v1alpha1.ApprovalRequest
	if err := r.Get(ctx, client.ObjectKeyFromObject(approval), &completed); err != nil {
		return client.IgnoreNotFound(err)
	}
	return r.recordAdmittedDecision(ctx, &completed)
}

// Terminal reconciliation repairs an audit failure without changing the decision.
func (r *ApprovalRequestReconciler) recordAdmittedDecision(ctx context.Context, approval *v1alpha1.ApprovalRequest) error {
	if approval.Status.DecisionRef == "" || approval.Status.DecisionUID == "" {
		return nil
	}
	var decision v1alpha1.ApprovalDecision
	if err := r.Get(ctx, client.ObjectKey{Namespace: approval.Namespace, Name: approval.Status.DecisionRef}, &decision); err != nil {
		return err
	}
	return audit.RecordApprovalAdmission(ctx, r.Audit, approval, &decision)
}

// Separate read failures from invalid bindings so temporary API errors never fail the gate.
// A nil attempt with no reason means its execution reference has not been published yet.
func (r *ApprovalRequestReconciler) approvalAttempt(ctx context.Context, approval *v1alpha1.ApprovalRequest) (*v1alpha1.StepAttempt, string, error) {
	var attempt v1alpha1.StepAttempt
	if err := r.Get(ctx, types.NamespacedName{Namespace: approval.Namespace, Name: approval.Spec.AttemptRef.Name}, &attempt); err != nil {
		return nil, "", client.IgnoreNotFound(err)
	}
	if approval.UID == "" || attempt.UID == "" || approval.Spec.AttemptRef.UID != attempt.UID ||
		!attempt.DeletionTimestamp.IsZero() || state.IsTerminal(attempt.Status.Phase) {
		return nil, "request references an invalid or terminal attempt", nil
	}
	if err := ValidateDomainBinding(&attempt, approval, approval.Spec.AttemptRef.Name, v1alpha1.ExecutionKindHumanGate, approval.Spec.WorkflowRef, approval.Spec.StepName, approval.Spec.Attempt); err != nil {
		return nil, err.Error(), nil
	}
	if attempt.Status.ExecutionRef == nil {
		return nil, "", nil
	}
	ref := attempt.Status.ExecutionRef
	if ref.APIVersion != v1alpha1.GroupVersion.String() || ref.Kind != "ApprovalRequest" || ref.Name != approval.Name {
		return nil, "attempt does not authorize this approval request", nil
	}
	return &attempt, "", nil
}

func validateApprovalDecision(approval *v1alpha1.ApprovalRequest, decision *v1alpha1.ApprovalDecision) string {
	owner := metav1.GetControllerOf(decision)
	if !decision.DeletionTimestamp.IsZero() || owner == nil || owner.UID != approval.UID || owner.Name != approval.Name ||
		owner.Kind != "ApprovalRequest" || owner.APIVersion != v1alpha1.GroupVersion.String() ||
		decision.Spec.ApprovalRequestRef != (v1alpha1.UIDReference{Name: approval.Name, UID: approval.UID}) ||
		decision.Spec.StepAttemptRef != approval.Spec.AttemptRef || decision.Spec.WorkflowRef != approval.Spec.WorkflowRef {
		return "decision is not bound to this request, attempt, and workflow"
	}
	if approval.Spec.Approval.Mode != v1alpha1.AnyOf || approval.Spec.Approval.DenyBehavior != "Fail" {
		return "only AnyOf approval with Fail denial behavior is supported"
	}
	if decision.Spec.Decision != v1alpha1.Approved && decision.Spec.Decision != v1alpha1.Denied {
		return "decision must be Approved or Denied"
	}
	if strings.TrimSpace(decision.Spec.Subject.SubjectId) == "" || decision.Spec.AuthoredAt == nil || decision.Spec.AuthoredAt.IsZero() {
		return "decision requires an authored subject and timestamp"
	}
	// These are the identity claims authenticated and persisted by the trusted API.
	for _, group := range approval.Spec.Approval.RequiredGroups {
		if group != "" && slices.Contains(decision.Spec.Subject.Groups, group) {
			return ""
		}
	}
	return "decision subject does not belong to a required approval group"
}

func (r *ApprovalRequestReconciler) finishApproval(approval *v1alpha1.ApprovalRequest, phase v1alpha1.ResourcePhase, reason, message, decisionRef string) {
	approval.Status.Phase = phase
	approval.Status.DecisionRef = decisionRef
	approval.Status.FailureReason = ""
	if phase == v1alpha1.PhaseFailed {
		approval.Status.FailureReason = reason
	}
	approval.Status.Retryable = false
	if approval.Status.CompletedAt == nil {
		now := r.approvalTime()
		approval.Status.CompletedAt = &now
	}
	approval.Status.ObservedGeneration = approval.Generation
	apiMeta.SetStatusCondition(&approval.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: state.ConditionStatus(phase), Reason: reason, Message: message, ObservedGeneration: approval.Generation,
	})
}

func (r *ApprovalRequestReconciler) approvalTime() metav1.Time {
	if r.Now != nil {
		return metav1.NewTime(r.Now())
	}
	return metav1.Now()
}
