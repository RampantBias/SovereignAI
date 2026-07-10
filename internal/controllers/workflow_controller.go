package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/domain/state"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	WorkflowFinalizer = "sovereign-ai.io/workflow-cleanup"
	LabelWorkflow     = "sovereign-ai.io/workflow-id"
	LabelStep         = "sovereign-ai.io/step-name"
	defaultVolumeSize = "250Mi"
)

type WorkflowReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Audit        audit.Recorder
	Now          func() time.Time
	StorageClass string
}

func (r *WorkflowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SovereignWorkflow{}).
		Owns(&v1alpha1.StepAttempt{}).
		Complete(r)
}

func (r *WorkflowReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	// Get workflow record
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, request.NamespacedName, &workflow); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if workflow has been deleted
	if !workflow.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&workflow, WorkflowFinalizer) {
			return ctrl.Result{}, r.updateWorkflow(ctx, request.NamespacedName, func(latest *v1alpha1.SovereignWorkflow) {
				controllerutil.RemoveFinalizer(latest, WorkflowFinalizer)
			})
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(&workflow, WorkflowFinalizer) {
		return ctrl.Result{}, r.updateWorkflow(ctx, request.NamespacedName, func(latest *v1alpha1.SovereignWorkflow) {
			controllerutil.AddFinalizer(latest, WorkflowFinalizer)
		})
	}

	// Check for namespace termination
	terminating, err := namespaceTerminating(ctx, r.Client, workflow.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if terminating {
		return ctrl.Result{}, nil
	}

	// Ensure workflow has steps
	if len(workflow.Spec.Steps) == 0 {
		return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "InvalidDefinition", "workflow must contain at least one step")
	}

	// Trigger new workflow into pending
	if workflow.Status.Phase == "" {
		_, err := r.updateWorkflowStatus(ctx, request.NamespacedName, func(latest *v1alpha1.SovereignWorkflow) {
			latest.Status.Phase = string(v1alpha1.PhasePending)
			latest.Status.ObservedGeneration = latest.Generation
		})
		return ctrl.Result{}, err
	}

	// Verify there is a pvc registered (skipping actual validation)
	if workflow.Status.PvcName == "" {
		return ctrl.Result{}, r.ensureWorkspace(ctx, &workflow)
	}

	// Check if workflow is being retried
	if workflow.Status.ActiveAttemptRef == "" {
		return ctrl.Result{}, r.createAttempt(ctx, &workflow, workflow.Spec.Steps[0], 1)
	}

	// Get current step attempt based on current step in workflow
	var attempt v1alpha1.StepAttempt
	key := types.NamespacedName{Namespace: workflow.Namespace, Name: workflow.Status.ActiveAttemptRef}
	if err := r.Get(ctx, key, &attempt); err != nil {
		if apierrors.IsNotFound(err) {
			step, found := findStep(workflow.Spec.Steps, workflow.Status.ActiveStepName)
			if !found {
				return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "StepMissing", workflow.Status.ActiveStepName)
			}
			var attempts v1alpha1.StepAttemptList
			if listErr := r.List(ctx, &attempts, client.InNamespace(workflow.Namespace), client.MatchingLabels{LabelWorkflow: workflow.Spec.WorkflowID, LabelStep: step.Name}); listErr != nil {
				return ctrl.Result{}, listErr
			}
			return ctrl.Result{}, r.createAttempt(ctx, &workflow, step, state.NextAttemptNumber(attempts.Items, step.Name))
		}
		return ctrl.Result{}, err
	}

	// Handle step transition
	switch attempt.Status.Phase {
	// On PhaseSucceeded, we either create an attempt for the next step or we mark workflow as succeeded
	case v1alpha1.PhaseSucceeded:
		next, found := nextStep(workflow.Spec.Steps, attempt.Spec.StepName)
		if !found {
			_, err := r.updateWorkflowStatus(ctx, request.NamespacedName, func(latest *v1alpha1.SovereignWorkflow) {
				latest.Status.Phase = string(v1alpha1.PhaseSucceeded)
				latest.Status.ActiveStepName = ""
				latest.Status.ActiveAttemptRef = ""
				latest.Status.ObservedGeneration = latest.Generation
			})
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.createAttempt(ctx, &workflow, next, 1)
	// On PhaseFailed or PhaseInterrupted, we need to evaluate whether retry is possible
	case v1alpha1.PhaseFailed, v1alpha1.PhaseInterrupted:
		// Locate current step
		step, found := findStep(workflow.Spec.Steps, attempt.Spec.StepName)
		if !found {
			return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "StepMissing", attempt.Spec.StepName)
		}

		// Catch-all for max attempt count to be minimum 1
		maxAttempts := step.MaxAttempts
		if maxAttempts == 0 {
			maxAttempts = 1
		}

		// If attempt is retryable append a recovery event and create a new attempt
		if attempt.Status.Retryable && attempt.Spec.Attempt < maxAttempts {
			if err := r.appendRecoveryEvents(ctx, &workflow, &attempt, step, attempt.Spec.Attempt+1); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.createAttempt(ctx, &workflow, step, attempt.Spec.Attempt+1)
		}
		return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "StepFailed", attempt.Status.FailureReason)
	}
	return ctrl.Result{}, nil
}

// Verifies valid PVC is referenced by a workflow
func (r *WorkflowReconciler) ensureWorkspace(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) error {
	name := workflow.Name + "-workspace"
	var existing corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Namespace: workflow.Namespace, Name: name}, &existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if apierrors.IsNotFound(err) {
		// Specify volume size
		size := workflow.Spec.RequestedVolumeSize
		if size == "" {
			size = defaultVolumeSize
		}
		quantity, err := resource.ParseQuantity(size)
		if err != nil {
			return r.failWorkflow(ctx, workflow, "InvalidWorkspaceSize", err.Error())
		}

		claim := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: workflow.Namespace, Labels: map[string]string{LabelWorkflow: workflow.Spec.WorkflowID}},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: quantity}},
			},
		}

		// Choose default storage class
		if r.StorageClass != "" {
			claim.Spec.StorageClassName = &r.StorageClass
		}

		// Set collapsing reference & create
		if err := controllerutil.SetControllerReference(workflow, claim, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, claim); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}

	// Updates PVC reference on workflow
	updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
		latest.Status.PvcName = name
	})
	if err != nil {
		return err
	}
	return r.appendWorkflowEvent(ctx, updated, "WorkspaceCreated", "", 0, "create", name, "created", "", map[string]string{"pvc": name}, map[string]string{"requestedVolumeSize": updated.Spec.RequestedVolumeSize})
}

// CreateAttempt generates a new StepAttempt CRD
func (r *WorkflowReconciler) createAttempt(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, step v1alpha1.StepConfig, number int32) error {
	name := attemptName(step.Name, number)
	attempt := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: workflow.Namespace,
			Labels:    map[string]string{LabelWorkflow: workflow.Spec.WorkflowID, LabelStep: step.Name},
		},
		Spec: v1alpha1.StepAttemptSpec{
			WorkflowRef:     workflow.Name,
			StepName:        step.Name,
			Attempt:         number,
			Kind:            step.Kind,
			Responsibility:  step.Responsibility,
			Image:           step.Image,
			Executable:      append([]string(nil), step.Executable...),
			Inputs:          append([]v1alpha1.ArtifactReference(nil), step.Inputs...),
			OutputContracts: append([]v1alpha1.ContractReference(nil), step.Outputs...),
			Capabilities:    append([]string(nil), step.Capabilities...),
			Timeout:         step.Timeout,
		},
	}
	if step.ModelName != "" {
		attempt.Spec.InferenceLeaseRef = name + "-inference"
		attempt.Spec.Inference = &v1alpha1.InferenceRequestSpec{
			Model: step.ModelName, ModelRevision: step.ModelRevision,
			EstimatedKVRAMMiB: step.RequestedVRAMAllocation,
			SharingScope:      step.SharingScope, Priority: step.Priority, Evictable: step.Evictable,
		}
	}
	if err := controllerutil.SetControllerReference(workflow, attempt, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, attempt); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
		latest.Status.Phase = string(v1alpha1.PhaseRunning)
		latest.Status.ActiveStepName = step.Name
		latest.Status.ActiveAttemptRef = name
		latest.Status.ObservedGeneration = latest.Generation
	})
	if err != nil {
		return err
	}
	return r.appendWorkflowEvent(ctx, updated, "StepAttemptCreated", step.Name, number, "create", name, "created", "", map[string]string{"attempt": name}, nil)
}

// Update Workflow status to failed
func (r *WorkflowReconciler) failWorkflow(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, reason, message string) error {
	updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
		latest.Status.Phase = string(v1alpha1.PhaseFailed)
		latest.Status.Conditions = []metav1.Condition{{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: message,
			ObservedGeneration: latest.Generation, LastTransitionTime: metav1.Now(),
		}}
	})
	if err != nil {
		return err
	}
	return r.appendWorkflowEvent(ctx, updated, "WorkflowFailed", updated.Status.ActiveStepName, 0, "complete", updated.Name, "failed", reason, nil, map[string]string{"message": message})
}

func (r *WorkflowReconciler) updateWorkflow(ctx context.Context, key types.NamespacedName, mutate func(*v1alpha1.SovereignWorkflow)) error {
	logger := ctrl.LoggerFrom(ctx).WithValues("workflow", key.String())
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.SovereignWorkflow
		if err := r.Get(ctx, key, &latest); err != nil {
			return err
		}
		mutate(&latest)
		if err := r.Update(ctx, &latest); err != nil {
			if apierrors.IsConflict(err) {
				logger.V(1).Info("retrying workflow update after conflict", "resourceVersion", latest.ResourceVersion)
			}
			return err
		}
		return nil
	})
}

func (r *WorkflowReconciler) updateWorkflowStatus(ctx context.Context, key types.NamespacedName, mutate func(*v1alpha1.SovereignWorkflow)) (*v1alpha1.SovereignWorkflow, error) {
	logger := ctrl.LoggerFrom(ctx).WithValues("workflow", key.String())
	var updated v1alpha1.SovereignWorkflow
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.SovereignWorkflow
		if err := r.Get(ctx, key, &latest); err != nil {
			return err
		}
		mutate(&latest)
		if err := r.Status().Update(ctx, &latest); err != nil {
			if apierrors.IsConflict(err) {
				logger.V(1).Info("retrying workflow status update after conflict", "resourceVersion", latest.ResourceVersion)
			}
			return err
		}
		updated = latest
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// Handles converted recovery/retry into workflow events
func (r *WorkflowReconciler) appendRecoveryEvents(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, failedAttempt *v1alpha1.StepAttempt, step v1alpha1.StepConfig, nextAttempt int32) error {
	references := map[string]string{
		"failedAttempt": failedAttempt.Name,
		"retryAttempt":  attemptName(step.Name, nextAttempt),
	}
	data := map[string]any{
		"retryFrom":        step.Name,
		"previousAttempt":  failedAttempt.Spec.Attempt,
		"nextAttempt":      nextAttempt,
		"failureReason":    failedAttempt.Status.FailureReason,
		"interruptedPhase": failedAttempt.Status.Phase,
	}
	if err := r.appendWorkflowEvent(ctx, workflow, "RecoveryDecisionSelected", step.Name, failedAttempt.Spec.Attempt, "select", step.Name, "selected", failedAttempt.Status.FailureReason, references, data); err != nil {
		return err
	}
	return r.appendWorkflowEvent(ctx, workflow, "StepAttemptRetried", step.Name, nextAttempt, "retry", attemptName(step.Name, nextAttempt), "created", failedAttempt.Status.FailureReason, references, data)
}

func (r *WorkflowReconciler) appendWorkflowEvent(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, eventType, step string, attempt int32, action, target, outcome, reason string, references map[string]string, data any) error {
	return appendControllerEvent(ctx, r.Audit, "workflow-controller", r.Now, audit.EventOptions{
		Type: eventType,
		Subject: audit.Subject{
			Project:   workflow.Spec.ProjectName,
			Namespace: workflow.Namespace,
			Workflow:  workflow.Name,
			Step:      step,
			Attempt:   attempt,
		},
		Action:        action,
		Target:        target,
		Outcome:       outcome,
		Reason:        reason,
		CorrelationID: workflow.Name,
		References:    references,
		Data:          data,
	})
}

func attemptName(step string, number int32) string {
	clean := strings.Trim(strings.ToLower(step), "-")
	clean = strings.ReplaceAll(clean, "_", "-")
	if len(clean) > 45 {
		clean = clean[:45]
	}
	return fmt.Sprintf("%s-%03d", clean, number)
}

func findStep(steps []v1alpha1.StepConfig, name string) (v1alpha1.StepConfig, bool) {
	for _, step := range steps {
		if step.Name == name {
			return step, true
		}
	}
	return v1alpha1.StepConfig{}, false
}

func nextStep(steps []v1alpha1.StepConfig, name string) (v1alpha1.StepConfig, bool) {
	for i, step := range steps {
		if step.Name == name && i+1 < len(steps) {
			return steps[i+1], true
		}
	}
	return v1alpha1.StepConfig{}, false
}
