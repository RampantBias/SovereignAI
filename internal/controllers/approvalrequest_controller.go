package controllers

import (
	"context"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ApprovalRequestReconciler owns the request side of human authority.
// add immutable ApprovalDecision observation; until then requests remain
// explicitly AwaitingApproval rather than mutating StepAttempt status directly.
type ApprovalRequestReconciler struct {
	client.Client
	Audit audit.Recorder
	Now   func() time.Time
}

func (r *ApprovalRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.ApprovalRequest{}).Complete(r)
}

func (r *ApprovalRequestReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var approval v1alpha1.ApprovalRequest
	if err := r.Get(ctx, request.NamespacedName, &approval); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !approval.DeletionTimestamp.IsZero() || terminalAttempt(approval.Status.Phase) {
		return ctrl.Result{}, nil
	}
	terminating, err := namespaceTerminating(ctx, r.Client, approval.Namespace)
	if err != nil || terminating {
		return ctrl.Result{}, err
	}
	if approval.Status.Phase == v1alpha1.PhaseAwaitingApproval {
		return ctrl.Result{}, nil
	}
	authorized, err := validateDomainAuthority(ctx, r.Client, &approval, approval.Spec.AttemptRef.Name, v1alpha1.ExecutionKindHumanGate, approval.Spec.WorkflowRef, approval.Spec.StepName, approval.Spec.Attempt)
	if err != nil {
		return ctrl.Result{}, r.setFailed(ctx, &approval, "InvalidStepAttemptAuthority")
	}
	if !authorized {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.setAwaiting(ctx, &approval); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, appendControllerEvent(ctx, r.Audit, "approvalrequest-controller", r.Now, audit.EventOptions{
		Type: "ApprovalRequested", Subject: audit.Subject{Namespace: approval.Namespace, Workflow: approval.Spec.WorkflowRef.Name, Step: approval.Spec.StepName, Attempt: approval.Spec.Attempt},
		Action: "request", Target: approval.Name, Outcome: "awaiting", References: map[string]string{"approvalRequest": approval.Name, "stepAttempt": approval.Spec.AttemptRef.Name},
		Data: map[string]any{"mode": approval.Spec.Approval.Mode, "requiredGroups": approval.Spec.Approval.RequiredGroups},
	})
}

func (r *ApprovalRequestReconciler) setFailed(ctx context.Context, approval *v1alpha1.ApprovalRequest, reason string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.ApprovalRequest
		if err := r.Get(ctx, types.NamespacedName{Namespace: approval.Namespace, Name: approval.Name}, &latest); err != nil {
			return err
		}
		latest.Status.Phase = v1alpha1.PhaseFailed
		latest.Status.FailureReason = reason
		latest.Status.Retryable = false
		latest.Status.ObservedGeneration = latest.Generation
		apiMeta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: "approval request is not authorized by its StepAttempt", ObservedGeneration: latest.Generation})
		return r.Status().Update(ctx, &latest)
	})
}

func (r *ApprovalRequestReconciler) setAwaiting(ctx context.Context, approval *v1alpha1.ApprovalRequest) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.ApprovalRequest
		if err := r.Get(ctx, types.NamespacedName{Namespace: approval.Namespace, Name: approval.Name}, &latest); err != nil {
			return err
		}
		if latest.Status.Phase == v1alpha1.PhaseAwaitingApproval {
			return nil
		}
		now := metav1.Now()
		if r.Now != nil {
			now = metav1.NewTime(r.Now())
		}
		latest.Status.Phase = v1alpha1.PhaseAwaitingApproval
		latest.Status.StartedAt = &now
		latest.Status.ObservedGeneration = latest.Generation
		apiMeta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: "Ready", Status: metav1.ConditionUnknown, Reason: "AwaitingDecision", Message: "waiting for an authoritative ApprovalDecision", ObservedGeneration: latest.Generation,
		})
		return r.Status().Update(ctx, &latest)
	})
}
