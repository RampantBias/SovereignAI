package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StepAttemptReconciler owns only workflow attempt lifecycle. Execution and
// authority belong to the domain resource referenced by status.executionRef.
type StepAttemptReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Audit  audit.Recorder
	Now    func() time.Time
}

func (r *StepAttemptReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.StepAttempt{}).
		Owns(&v1alpha1.AgentRun{}).
		Owns(&v1alpha1.UtilityOperation{}).
		Owns(&v1alpha1.ApprovalRequest{}).
		Owns(&v1alpha1.ValidationRun{}).
		Complete(r)
}

func (r *StepAttemptReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var attempt v1alpha1.StepAttempt
	if err := r.Get(ctx, request.NamespacedName, &attempt); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !attempt.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	terminating, err := namespaceTerminating(ctx, r.Client, attempt.Namespace)
	if err != nil || terminating {
		return ctrl.Result{}, err
	}
	if attempt.Status.Phase == "" {
		return ctrl.Result{}, r.setPhase(ctx, &attempt, v1alpha1.PhasePending, "Initialized", "attempt initialized")
	}
	if terminalAttempt(attempt.Status.Phase) {
		return ctrl.Result{}, nil
	}
	if attempt.Status.ExecutionRef == nil {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if attempt.Status.ExecutionRef.APIVersion != v1alpha1.GroupVersion.String() || attempt.Status.ExecutionRef.Kind != domainKind(attempt.Spec.Kind) {
		return ctrl.Result{}, r.fail(ctx, &attempt, "InvalidExecutionReference", false)
	}
	phase, reason, retryable, err := r.domainStatus(ctx, &attempt)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.interrupt(ctx, &attempt, "ExecutionPrimitiveLost", true)
		}
		return ctrl.Result{}, err
	}
	if phase == "" {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	attempt.Status.FailureReason = reason
	attempt.Status.Retryable = retryable
	return ctrl.Result{}, r.setPhase(ctx, &attempt, phase, domainPhaseReason(phase, reason), domainPhaseMessage(attempt.Status.ExecutionRef.Kind, phase))
}

func (r *StepAttemptReconciler) domainStatus(ctx context.Context, attempt *v1alpha1.StepAttempt) (v1alpha1.ResourcePhase, string, bool, error) {
	key := types.NamespacedName{Namespace: attempt.Namespace, Name: attempt.Status.ExecutionRef.Name}
	switch attempt.Spec.Kind {
	case v1alpha1.ExecutionKindAgent:
		if attempt.Status.ExecutionRef.Kind != "AgentRun" {
			return "", "", false, fmt.Errorf("agent attempt references %s", attempt.Status.ExecutionRef.Kind)
		}
		var run v1alpha1.AgentRun
		if err := r.Get(ctx, key, &run); err != nil {
			return "", "", false, err
		}
		if err := validateDomainBinding(attempt, &run, run.Spec.AttemptRef, v1alpha1.ExecutionKindAgent, run.Spec.WorkflowRef, run.Spec.StepName, run.Spec.Attempt); err != nil {
			return v1alpha1.PhaseFailed, "InvalidDomainAuthority", false, nil
		}
		return run.Status.Phase, run.Status.FailureReason, run.Status.Retryable, nil
	case v1alpha1.ExecutionKindUtility:
		if attempt.Status.ExecutionRef.Kind != "UtilityOperation" {
			return "", "", false, fmt.Errorf("utility attempt references %s", attempt.Status.ExecutionRef.Kind)
		}
		var operation v1alpha1.UtilityOperation
		if err := r.Get(ctx, key, &operation); err != nil {
			return "", "", false, err
		}
		if err := validateDomainBinding(attempt, &operation, operation.Spec.AttemptRef, v1alpha1.ExecutionKindUtility, operation.Spec.WorkflowRef, operation.Spec.StepName, operation.Spec.Attempt); err != nil {
			return v1alpha1.PhaseFailed, "InvalidDomainAuthority", false, nil
		}
		return operation.Status.Phase, operation.Status.FailureReason, operation.Status.Retryable, nil
	case v1alpha1.ExecutionKindHumanGate:
		if attempt.Status.ExecutionRef.Kind != "ApprovalRequest" {
			return "", "", false, fmt.Errorf("human gate attempt references %s", attempt.Status.ExecutionRef.Kind)
		}
		var approval v1alpha1.ApprovalRequest
		if err := r.Get(ctx, key, &approval); err != nil {
			return "", "", false, err
		}
		if err := validateDomainBinding(attempt, &approval, approval.Spec.AttemptRef, v1alpha1.ExecutionKindHumanGate, approval.Spec.WorkflowRef, approval.Spec.StepName, approval.Spec.Attempt); err != nil {
			return v1alpha1.PhaseFailed, "InvalidDomainAuthority", false, nil
		}
		return approval.Status.Phase, approval.Status.FailureReason, approval.Status.Retryable, nil
	case v1alpha1.ExecutionKindValidation:
		if attempt.Status.ExecutionRef.Kind != "ValidationRun" {
			return "", "", false, fmt.Errorf("validation attempt references %s", attempt.Status.ExecutionRef.Kind)
		}
		var run v1alpha1.ValidationRun
		if err := r.Get(ctx, key, &run); err != nil {
			return "", "", false, err
		}
		if err := validateDomainBinding(attempt, &run, run.Spec.AttemptRef, v1alpha1.ExecutionKindValidation, run.Spec.WorkflowRef, run.Spec.StepName, run.Spec.Attempt); err != nil {
			return v1alpha1.PhaseFailed, "InvalidDomainAuthority", false, nil
		}
		return run.Status.Phase, run.Status.FailureReason, run.Status.Retryable, nil
	default:
		return v1alpha1.PhaseFailed, "UnsupportedKind", false, nil
	}
}

func domainPhaseReason(phase v1alpha1.ResourcePhase, failureReason string) string {
	if failureReason != "" {
		return failureReason
	}
	switch phase {
	case v1alpha1.PhaseAwaitingApproval:
		return "ApprovalRequested"
	case v1alpha1.PhaseSucceeded:
		return "DomainExecutionSucceeded"
	case v1alpha1.PhaseFailed:
		return "DomainExecutionFailed"
	case v1alpha1.PhaseInterrupted:
		return "DomainExecutionInterrupted"
	default:
		return "DomainExecutionObserved"
	}
}

func domainPhaseMessage(kind string, phase v1alpha1.ResourcePhase) string {
	return fmt.Sprintf("%s is %s", kind, phase)
}

func (r *StepAttemptReconciler) setPhase(ctx context.Context, attempt *v1alpha1.StepAttempt, phase v1alpha1.ResourcePhase, reason, message string) error {
	updated, changed, err := r.updateAttemptStatus(ctx, client.ObjectKeyFromObject(attempt), func(latest *v1alpha1.StepAttempt) bool {
		if terminalAttempt(latest.Status.Phase) && latest.Status.Phase != phase {
			return false
		}
		condition := apiMeta.FindStatusCondition(latest.Status.Conditions, "Ready")
		if latest.Status.Phase == phase && condition != nil && condition.Reason == reason && condition.Message == message {
			return false
		}
		now := metav1.Now()
		if r.Now != nil {
			now = metav1.NewTime(r.Now())
		}
		latest.Status.Phase = phase
		latest.Status.FailureReason = attempt.Status.FailureReason
		latest.Status.Retryable = attempt.Status.Retryable
		latest.Status.ObservedGeneration = latest.Generation
		if phase == v1alpha1.PhaseRunning && latest.Status.StartedAt == nil {
			latest.Status.StartedAt = &now
		}
		if terminalAttempt(phase) {
			latest.Status.CompletedAt = &now
		}
		apiMeta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{Type: "Ready", Status: conditionStatus(phase), Reason: reason, Message: message, ObservedGeneration: latest.Generation})
		return true
	})
	if err != nil || !changed {
		return err
	}
	return r.appendPhaseEvent(ctx, updated, phase, reason)
}

func (r *StepAttemptReconciler) fail(ctx context.Context, attempt *v1alpha1.StepAttempt, reason string, retryable bool) error {
	attempt.Status.FailureReason = reason
	attempt.Status.Retryable = retryable
	return r.setPhase(ctx, attempt, v1alpha1.PhaseFailed, reason, "attempt failed")
}

func (r *StepAttemptReconciler) interrupt(ctx context.Context, attempt *v1alpha1.StepAttempt, reason string, retryable bool) error {
	attempt.Status.FailureReason = reason
	attempt.Status.Retryable = retryable
	return r.setPhase(ctx, attempt, v1alpha1.PhaseInterrupted, reason, "attempt interrupted")
}

func (r *StepAttemptReconciler) updateAttemptStatus(ctx context.Context, key types.NamespacedName, mutate func(*v1alpha1.StepAttempt) bool) (*v1alpha1.StepAttempt, bool, error) {
	var updated v1alpha1.StepAttempt
	changed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.StepAttempt
		if err := r.Get(ctx, key, &latest); err != nil {
			return err
		}
		if !mutate(&latest) {
			updated = latest
			return nil
		}
		if err := r.Status().Update(ctx, &latest); err != nil {
			return err
		}
		updated, changed = latest, true
		return nil
	})
	return &updated, changed, err
}

func (r *StepAttemptReconciler) appendPhaseEvent(ctx context.Context, attempt *v1alpha1.StepAttempt, phase v1alpha1.ResourcePhase, reason string) error {
	if r.Audit == nil {
		return nil
	}
	references := map[string]string{}
	if attempt.Status.ExecutionRef != nil {
		references["executionKind"] = attempt.Status.ExecutionRef.Kind
		references["execution"] = attempt.Status.ExecutionRef.Name
	}
	return appendControllerEvent(ctx, r.Audit, "stepattempt-controller", r.Now, audit.EventOptions{
		Type:    "StepAttempt" + string(phase),
		Subject: audit.Subject{Namespace: attempt.Namespace, Workflow: attempt.Spec.WorkflowRef, Step: attempt.Spec.StepName, Attempt: attempt.Spec.Attempt},
		Action:  "observe", Target: attempt.Name, Outcome: string(phase), Reason: reason, References: references,
	})
}

func terminalAttempt(phase v1alpha1.ResourcePhase) bool {
	switch phase {
	case v1alpha1.PhaseSucceeded, v1alpha1.PhaseFailed, v1alpha1.PhaseCancelled, v1alpha1.PhaseInterrupted:
		return true
	default:
		return false
	}
}

func conditionStatus(phase v1alpha1.ResourcePhase) metav1.ConditionStatus {
	switch phase {
	case v1alpha1.PhaseSucceeded:
		return metav1.ConditionTrue
	case v1alpha1.PhaseFailed, v1alpha1.PhaseCancelled, v1alpha1.PhaseInterrupted:
		return metav1.ConditionFalse
	default:
		return metav1.ConditionUnknown
	}
}
