package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/domain/state"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
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
	client.Reader
	Scheme         *runtime.Scheme
	Audit          audit.Recorder
	Now            func() time.Time
	StorageClass   string
	BootstrapImage string
}

func (r *WorkflowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SovereignWorkflow{}).
		Owns(&v1alpha1.StepAttempt{}).
		Owns(&coordinationv1.Lease{}).
		Owns(&batchv1.Job{}).
		Owns(&v1alpha1.Artifact{}).
		Complete(r)
}

func (r *WorkflowReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	// Get workflow record
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Client.Get(ctx, request.NamespacedName, &workflow); err != nil {
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
	if workflow.Status.WorkspaceWriterLeaseRef == "" {
		return ctrl.Result{}, r.ensureWorkspaceWriterLease(ctx, &workflow)
	}
	bootstrapReady, bootstrapResult, err := r.reconcileBootstrap(ctx, &workflow)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !bootstrapReady {
		return bootstrapResult, nil
	}

	// Check if workflow is being retried
	if workflow.Status.ActiveAttemptRef == "" {
		return ctrl.Result{}, r.createAttempt(ctx, &workflow, workflow.Spec.Steps[0], 1)
	}

	// Get current step attempt based on current step in workflow
	var attempt v1alpha1.StepAttempt
	key := types.NamespacedName{Namespace: workflow.Namespace, Name: workflow.Status.ActiveAttemptRef}
	if err := r.stepAttemptReader().Get(ctx, key, &attempt); err != nil {
		if apierrors.IsNotFound(err) {
			step, found := findStep(workflow.Spec.Steps, workflow.Status.ActiveStepName)
			if !found {
				return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "StepMissing", workflow.Status.ActiveStepName)
			}
			var attempts v1alpha1.StepAttemptList
			if listErr := r.stepAttemptReader().List(ctx, &attempts, client.InNamespace(workflow.Namespace), client.MatchingLabels{LabelWorkflow: workflow.Spec.WorkflowID, LabelStep: step.Name}); listErr != nil {
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
			return ctrl.Result{}, r.createAttemptWithFeedback(ctx, &workflow, step, attempt.Spec.Attempt+1, retryFeedbackForAttempt(&attempt))
		}
		return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "StepFailed", attempt.Status.FailureReason)
	}
	return ctrl.Result{}, nil
}

// ensureWorkspaceWriterLease creates the workflow-owned coordination
// primitive that serializes every writable workspace mount. LeaseTransitions
// is retained across releases and serves as the monotonically increasing
// writer epoch.
func (r *WorkflowReconciler) ensureWorkspaceWriterLease(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) error {
	name := workspaceWriterLeaseName(workflow.Name)
	var lease coordinationv1.Lease
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: workflow.Namespace, Name: name}, &lease)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if apierrors.IsNotFound(err) {
		zero := int32(0)
		lease = coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: workflow.Namespace,
				Labels: map[string]string{LabelWorkflow: workflow.Spec.WorkflowID},
			},
			Spec: coordinationv1.LeaseSpec{LeaseTransitions: &zero},
		}
		if err := controllerutil.SetControllerReference(workflow, &lease, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &lease); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	} else if !metav1.IsControlledBy(&lease, workflow) {
		return r.failWorkflow(ctx, workflow, "InvalidWorkspaceWriterLease", fmt.Sprintf("lease %s is not controlled by workflow %s", name, workflow.Name))
	}

	updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
		latest.Status.WorkspaceWriterLeaseRef = name
	})
	if err != nil {
		return err
	}
	return r.appendWorkflowEvent(ctx, updated, "WorkspaceWriterLeaseCreated", "", 0, "create", name, "created", "", map[string]string{"lease": name}, map[string]any{"writerEpoch": 0})
}

// Verifies valid PVC is referenced by a workflow
func (r *WorkflowReconciler) ensureWorkspace(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) error {
	name := workflow.Name + "-workspace"
	var existing corev1.PersistentVolumeClaim
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: workflow.Namespace, Name: name}, &existing)
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
	return r.createAttemptWithFeedback(ctx, workflow, step, number, nil)
}

func (r *WorkflowReconciler) createAttemptWithFeedback(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, step v1alpha1.StepConfig, number int32, feedback *v1alpha1.FailedAgentAttempt) error {
	name := attemptName(step.Name, number)
	attempt := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: workflow.Namespace,
			Labels:    map[string]string{LabelWorkflow: workflow.Spec.WorkflowID, LabelStep: step.Name},
		},
		Spec: v1alpha1.StepAttemptSpec{
			WorkflowRef: v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID},
			StepName:    step.Name,
			Attempt:     number,
			Kind:        step.Kind,
		},
	}
	if err := controllerutil.SetControllerReference(workflow, attempt, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, attempt); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	if err := r.stepAttemptReader().Get(ctx, types.NamespacedName{Namespace: attempt.Namespace, Name: attempt.Name}, attempt); err != nil {
		return err
	}
	executionRef, err := r.ensureDomainExecution(ctx, attempt, step, feedback)
	if err != nil {
		return err
	}
	if err := r.recordAttemptExecutionRef(ctx, attempt, executionRef); err != nil {
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

func (r *WorkflowReconciler) ensureDomainExecution(ctx context.Context, attempt *v1alpha1.StepAttempt, step v1alpha1.StepConfig, feedback *v1alpha1.FailedAgentAttempt) (*v1alpha1.TypedLocalReference, error) {
	metadata := metav1.ObjectMeta{
		Name: attempt.Name, Namespace: attempt.Namespace,
		Labels: map[string]string{LabelWorkflow: attempt.Labels[LabelWorkflow], LabelStep: step.Name},
	}
	var object client.Object
	var reference v1alpha1.TypedLocalReference
	switch step.Kind {
	case v1alpha1.ExecutionKindAgent:
		if step.Agent == nil {
			return nil, fmt.Errorf("agent step %s has no agent specification", step.Name)
		}
		object = &v1alpha1.AgentRun{ObjectMeta: metadata, Spec: v1alpha1.AgentRunSpec{
			AttemptRef: attempt.Name, WorkflowRef: attempt.Spec.WorkflowRef, StepName: step.Name, Attempt: attempt.Spec.Attempt,
			PriorAttemptRef: copyFailedAgentAttempt(feedback),
			Responsibility:  step.Agent.Responsibility, Image: step.Agent.Image,
			Executable: append([]string(nil), step.Agent.Executable...), Capabilities: append([]string(nil), step.Agent.Capabilities...),
			Inputs: append([]v1alpha1.ArtifactReference(nil), step.Inputs...), OutputContracts: append([]v1alpha1.ContractReference(nil), step.Outputs...),
			Inference: copyInferenceRequest(step.Agent.Inference), Timeout: step.Timeout,
		}}
		reference = v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: attempt.Name}
	case v1alpha1.ExecutionKindUtility:
		if step.Utility == nil {
			return nil, fmt.Errorf("utility step %s has no utility operation", step.Name)
		}
		object = &v1alpha1.UtilityOperation{ObjectMeta: metadata, Spec: v1alpha1.UtilityOperationSpec{
			AttemptRef: attempt.Name, WorkflowRef: attempt.Spec.WorkflowRef, StepName: step.Name, Attempt: attempt.Spec.Attempt,
			Operation: copyUtilityOperation(*step.Utility), Inputs: append([]v1alpha1.ArtifactReference(nil), step.Inputs...),
			OutputContracts: append([]v1alpha1.ContractReference(nil), step.Outputs...), Timeout: step.Timeout,
		}}
		reference = v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "UtilityOperation", Name: attempt.Name}
	case v1alpha1.ExecutionKindHumanGate:
		if step.Approval == nil {
			return nil, fmt.Errorf("human gate %s has no approval specification", step.Name)
		}
		object = &v1alpha1.ApprovalRequest{ObjectMeta: metadata, Spec: v1alpha1.ApprovalRequestSpec{
			AttemptRef: v1alpha1.UIDReference{Name: attempt.Name}, WorkflowRef: attempt.Spec.WorkflowRef, StepName: step.Name,
			Attempt: attempt.Spec.Attempt, Approval: copyApprovalSpec(*step.Approval),
		}}
		reference = v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ApprovalRequest", Name: attempt.Name}
	case v1alpha1.ExecutionKindValidation:
		if step.Validation == nil {
			return nil, fmt.Errorf("validation step %s has no validation specification", step.Name)
		}
		object = &v1alpha1.ValidationRun{ObjectMeta: metadata, Spec: v1alpha1.ValidationRunSpec{
			AttemptRef: attempt.Name, WorkflowRef: attempt.Spec.WorkflowRef, StepName: step.Name, Attempt: attempt.Spec.Attempt,
			Provider: step.Validation.Provider, Commit: step.Validation.Commit, ImageDigest: step.Validation.ImageDigest,
			OverlayPath: step.Validation.OverlayPath, Destination: step.Validation.Destination,
		}}
		reference = v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ValidationRun", Name: attempt.Name}
	default:
		return nil, fmt.Errorf("unsupported execution kind %q", step.Kind)
	}
	if err := controllerutil.SetControllerReference(attempt, object, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, object); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return &reference, nil
}

func retryFeedbackForAttempt(attempt *v1alpha1.StepAttempt) *v1alpha1.FailedAgentAttempt {
	if attempt.Spec.Kind != v1alpha1.ExecutionKindAgent || attempt.Status.FailureReason == "" || attempt.Status.FailureMessage == "" {
		return nil
	}
	feedback := agentcontract.RetryFeedback{
		PreviousAttemptRef: attempt.Name,
		Code:               attempt.Status.FailureReason,
		Message:            agentcontract.SanitizeRetryFeedbackMessage(attempt.Status.FailureMessage),
	}
	if err := feedback.Validate(); err != nil {
		return nil
	}
	return &v1alpha1.FailedAgentAttempt{
		PreviousAttemptRef: feedback.PreviousAttemptRef,
		Code:               feedback.Code,
		Message:            feedback.Message,
	}
}

func copyFailedAgentAttempt(source *v1alpha1.FailedAgentAttempt) *v1alpha1.FailedAgentAttempt {
	if source == nil {
		return nil
	}
	result := *source
	return &result
}

func copyUtilityOperation(source v1alpha1.UtilityOperationRequest) v1alpha1.UtilityOperationRequest {
	result := v1alpha1.UtilityOperationRequest{Name: source.Name}
	if source.Parameters != nil {
		result.Parameters = make(map[string]string, len(source.Parameters))
		for name, value := range source.Parameters {
			result.Parameters[name] = value
		}
	}
	return result
}

func copyInferenceRequest(source *v1alpha1.InferenceRequestSpec) *v1alpha1.InferenceRequestSpec {
	if source == nil {
		return nil
	}
	result := *source
	return &result
}

func copyApprovalSpec(source v1alpha1.ApprovalSpec) v1alpha1.ApprovalSpec {
	result := source
	result.RequiredGroups = append([]string(nil), source.RequiredGroups...)
	return result
}

func (r *WorkflowReconciler) recordAttemptExecutionRef(ctx context.Context, attempt *v1alpha1.StepAttempt, reference *v1alpha1.TypedLocalReference) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.StepAttempt
		if err := r.stepAttemptReader().Get(ctx, client.ObjectKeyFromObject(attempt), &latest); err != nil {
			return err
		}
		if latest.Status.ExecutionRef != nil && *latest.Status.ExecutionRef == *reference {
			return nil
		}
		latest.Status.ExecutionRef = reference
		latest.Status.ObservedGeneration = latest.Generation
		return r.Status().Update(ctx, &latest)
	})
}

func (r *WorkflowReconciler) stepAttemptReader() client.Reader {
	if r.Reader != nil {
		return r.Reader
	}
	return r.Client
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
		if err := r.Client.Get(ctx, key, &latest); err != nil {
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
		if err := r.Client.Get(ctx, key, &latest); err != nil {
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
			Project:   workflow.Spec.Project.Name,
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
