package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/domain/state"
	"github.com/SovereignAI/internal/utilitycontract"
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

type WorkflowReconciler struct {
	client.Client
	client.Reader
	Scheme         *runtime.Scheme
	Audit          audit.Recorder
	Now            func() time.Time
	StorageClass   string
	BootstrapImage string
}

const (
	defaultVolumeSize = "250Mi"
)

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
	isDeleting, err := r.isDeleted(ctx, workflow, request)
	if isDeleting {
		return ctrl.Result{}, err
	}

	// Failed/interrupted workflows must not restart bootstrap or overwrite the
	// original terminal failure by trying to recover an already released writer.
	if phase := v1alpha1.ResourcePhase(workflow.Status.Phase); state.IsTerminal(phase) && phase != v1alpha1.PhaseSucceeded {
		return ctrl.Result{}, nil
	}

	// Ensure workflow has steps
	if len(workflow.Spec.Steps) == 0 {
		return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "InvalidDefinition", "workflow must contain at least one step")
	}

	if err := v1alpha1.ValidateApprovalRequirements(workflow.Spec.Steps); err != nil {
		return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "InvalidApprovalBinding", err.Error())
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

	// Ensure writer lease (write capability) is established for workflow
	if workflow.Status.WorkspaceWriterLeaseRef == "" {
		return ctrl.Result{}, r.ensureWorkspaceWriterLease(ctx, &workflow)
	}

	//
	bootstrapReady, bootstrapResult, err := r.ensureBootstrap(ctx, &workflow)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !bootstrapReady {
		return bootstrapResult, nil
	}

	if err := r.recordWorkflowRecovery(ctx, &workflow); err != nil {
		return ctrl.Result{}, err
	}

	// Check if workflow is terminal
	if state.IsTerminal(v1alpha1.ResourcePhase(workflow.Status.Phase)) {
		if workflow.Status.Phase == string(v1alpha1.PhaseSucceeded) {
			return ctrl.Result{}, r.appendWorkflowCompleted(ctx, &workflow, nil)
		}
		return ctrl.Result{}, nil
	}

	// Check if workflow is being retried
	if workflow.Status.ActiveAttemptRef == "" {
		step := workflow.Spec.Steps[0]

		if workflow.Status.ActiveStepName != "" {
			var found bool
			step, found = findStep(
				workflow.Spec.Steps,
				workflow.Status.ActiveStepName,
			)
			if !found {
				return ctrl.Result{}, r.failWorkflow(
					ctx,
					&workflow,
					"StepMissing",
					workflow.Status.ActiveStepName,
				)
			}
		}

		return ctrl.Result{}, r.createAttempt(ctx, &workflow, step, 1)
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
			if listErr := r.stepAttemptReader().List(
				ctx,
				&attempts,
				client.InNamespace(workflow.Namespace),
				client.MatchingLabels{
					controllermeta.LabelWorkflow: workflow.Spec.WorkflowID,
					controllermeta.LabelStep:     step.Name}); listErr != nil {
				return ctrl.Result{}, listErr
			}
			return ctrl.Result{}, r.createAttempt(ctx, &workflow, step, state.NextRetryNumber(attempts.Items, step.Name, workflow.Status.WorkflowAttempt))
		}
		return ctrl.Result{}, err
	}

	// Handle step transition
	switch attempt.Status.Phase {
	case v1alpha1.PhaseAwaitingApproval:
		// The active attempt carries approval state from its ApprovalRequest.
		// Preserve the gate as active until that attempt completes.
		if workflow.Status.Phase == string(v1alpha1.PhaseAwaitingApproval) && workflow.Status.ObservedGeneration == workflow.Generation {
			return ctrl.Result{}, nil
		}
		_, err := r.updateWorkflowStatus(ctx, request.NamespacedName, func(latest *v1alpha1.SovereignWorkflow) {
			latest.Status.Phase = string(v1alpha1.PhaseAwaitingApproval)
			latest.Status.ObservedGeneration = latest.Generation
		})
		return ctrl.Result{}, err
	// On PhaseSucceeded, we either create an attempt for the next step or we mark workflow as succeeded
	case v1alpha1.PhaseSucceeded:
		next, found := nextStep(workflow.Spec.Steps, attempt.Spec.StepName)
		if !found {
			finalStep, stepFound := findStep(workflow.Spec.Steps, attempt.Spec.StepName)
			if !stepFound {
				return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "StepMissing", attempt.Spec.StepName)
			}
			outputsReady, invalidReason, err := r.finalStepOutputsAccepted(ctx, &workflow, &attempt, finalStep)
			if err != nil {
				return ctrl.Result{}, err
			}
			if invalidReason != "" {
				return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "FinalOutputRejected", invalidReason)
			}
			if !outputsReady {
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			updated, err := r.updateWorkflowStatus(ctx, request.NamespacedName, func(latest *v1alpha1.SovereignWorkflow) {
				latest.Status.Phase = string(v1alpha1.PhaseSucceeded)
				latest.Status.ActiveStepName = ""
				latest.Status.ActiveAttemptRef = ""
				latest.Status.ObservedGeneration = latest.Generation
			})
			if err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.appendWorkflowCompleted(ctx, updated, &attempt)
		}
		return ctrl.Result{}, r.createAttempt(ctx, &workflow, next, 1)
	// On PhaseFailed or PhaseInterrupted, we need to evaluate whether retry is possible
	case v1alpha1.PhaseFailed, v1alpha1.PhaseInterrupted:
		// Locate current step
		step, found := findStep(workflow.Spec.Steps, attempt.Spec.StepName)
		if !found {
			return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "StepMissing", attempt.Spec.StepName)
		}

		decision, err := decideFailure(workflow.Spec.Steps, step, attempt.Status.Phase)
		if err != nil {
			return ctrl.Result{}, r.failWorkflow(
				ctx,
				&workflow,
				"InvalidRecoveryPolicy",
				err.Error(),
			)
		}

		switch decision.Action {
		case failureActionRetryWorkflow:
			if workflow.Spec.MaxWorkflowAttempt == 0 ||
				workflow.Status.WorkflowAttempt >= workflow.Spec.MaxWorkflowAttempt {
				return ctrl.Result{}, r.failWorkflow(
					ctx,
					&workflow,
					"WorkflowRetriesExhausted",
					attempt.Status.FailureReason,
				)
			}

			return ctrl.Result{}, r.beginWorkflowRetry(
				ctx,
				&workflow,
				&attempt,
				decision.RestartStep,
			)

		case failureActionFailWorkflow:
			return ctrl.Result{}, r.failWorkflow(
				ctx,
				&workflow,
				"StepFailed",
				attempt.Status.FailureReason,
			)

		case failureActionRetryStep:
			// Catch-all for max attempt count to be minimum 1
			maxAttempts := step.MaxAttempts
			if maxAttempts == 0 {
				maxAttempts = 1
			}

			// If attempt is retryable append a recovery event and create a new attempt
			if attempt.Status.Retryable && attempt.Spec.RetryNumber < maxAttempts {
				if err := r.appendRecoveryEvents(ctx, &workflow, &attempt, step, attempt.Spec.RetryNumber+1); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, r.createAttemptWithFeedback(ctx, &workflow, step, attempt.Spec.RetryNumber+1, retryFeedbackForAttempt(&attempt))
			}
			return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "StepFailed", attempt.Status.FailureReason)
		}
	}
	return ctrl.Result{}, nil
}

func (r *WorkflowReconciler) finalStepOutputsAccepted(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, attempt *v1alpha1.StepAttempt, step v1alpha1.StepConfig) (bool, string, error) {
	if len(step.Outputs) == 0 {
		return true, "", nil
	}
	producerName := attempt.Name
	if attempt.Status.ExecutionRef != nil {
		producerName = attempt.Status.ExecutionRef.Name
	}
	var artifactList v1alpha1.ArtifactList
	if err := r.Client.List(ctx, &artifactList, client.InNamespace(workflow.Namespace)); err != nil {
		return false, "", err
	}
	for _, output := range step.Outputs {
		acceptedCount := 0
		for index := range artifactList.Items {
			artifact := &artifactList.Items[index]
			if artifact.Spec.WorkflowRef != attempt.Spec.WorkflowRef || artifact.Spec.ProducerRef.Name != producerName || artifact.Spec.Contract != output {
				continue
			}
			if artifact.Status.Phase == v1alpha1.PhaseFailed {
				return false, fmt.Sprintf("final output %s/%s was rejected", output.Name, output.Version), nil
			}
			if artifacts.ArtifactAccepted(artifact) {
				acceptedCount++
			}
		}
		if acceptedCount == 0 {
			return false, "", nil
		}
		if acceptedCount > 1 {
			return false, fmt.Sprintf("final output %s/%s resolved to %d accepted artifacts", output.Name, output.Version, acceptedCount), nil
		}
	}
	return true, "", nil
}

func (r *WorkflowReconciler) isDeleted(ctx context.Context, workflow v1alpha1.SovereignWorkflow, request ctrl.Request) (bool, error) {
	if !workflow.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&workflow, controllermeta.WorkflowFinalizer) {
			return true, r.updateWorkflow(ctx, request.NamespacedName, func(latest *v1alpha1.SovereignWorkflow) {
				controllerutil.RemoveFinalizer(latest, controllermeta.WorkflowFinalizer)
			})
		}
		return true, nil
	}
	if !controllerutil.ContainsFinalizer(&workflow, controllermeta.WorkflowFinalizer) {
		return true, r.updateWorkflow(ctx, request.NamespacedName, func(latest *v1alpha1.SovereignWorkflow) {
			controllerutil.AddFinalizer(latest, controllermeta.WorkflowFinalizer)
		})
	}

	// Check for namespace termination
	terminating, err := controllers.NamespaceTerminating(ctx, r.Client, workflow.Namespace)
	if err != nil {
		return true, err
	}
	if terminating {
		return true, nil
	}
	return false, nil
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
				Labels: map[string]string{controllermeta.LabelWorkflow: workflow.Spec.WorkflowID},
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
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: workflow.Namespace, Labels: map[string]string{controllermeta.LabelWorkflow: workflow.Spec.WorkflowID}},
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

func (r *WorkflowReconciler) beginWorkflowRetry(
	ctx context.Context,
	workflow *v1alpha1.SovereignWorkflow,
	failed *v1alpha1.StepAttempt,
	restartStep string,
) error {
	evidence, err := r.captureWorkflowRecovery(ctx, workflow, failed, restartStep)
	if err != nil {
		return err
	}
	expectedWorkflowAttempt := failed.Spec.WorkflowAttempt

	updated, err := r.updateWorkflowStatus(
		ctx,
		client.ObjectKeyFromObject(workflow),
		func(latest *v1alpha1.SovereignWorkflow) {
			// Makes repeated reconciles idempotent.
			if latest.UID != workflow.UID || !latest.DeletionTimestamp.IsZero() ||
				state.IsTerminal(v1alpha1.ResourcePhase(latest.Status.Phase)) || latest.Status.ActiveAttemptRef != failed.Name ||
				latest.Status.WorkflowAttempt != expectedWorkflowAttempt {
				return
			}

			latest.Status.WorkflowAttempt++
			latest.Status.ActiveStepName = restartStep
			latest.Status.ActiveAttemptRef = ""
			latest.Status.Phase = string(v1alpha1.PhaseRunning)
			latest.Status.ObservedGeneration = latest.Generation
			latest.Status.Refinement = &v1alpha1.WorkflowRefinementStatus{
				Recovery:          evidence,
				Iteration:         latest.Status.WorkflowAttempt,
				TriggerStepName:   failed.Spec.StepName,
				TriggerAttemptRef: failed.Name,
				RestartStepName:   restartStep,
			}
		},
	)
	if err != nil {
		return err
	}
	return r.recordWorkflowRecovery(ctx, updated)
}

func (r *WorkflowReconciler) createAttemptWithFeedback(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, step v1alpha1.StepConfig, number int32, feedback *v1alpha1.FailedAgentAttempt) error {
	if err := r.recordWorkflowRecovery(ctx, workflow); err != nil {
		return err
	}
	origin, err := r.workflowRetryFeedback(ctx, workflow, step)
	if err != nil {
		return err
	}
	feedback = mergeRetryFeedback(origin, feedback)
	name := attemptName(step.Name, workflow.Status.WorkflowAttempt, number)
	attempt := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: workflow.Namespace,
			Labels: map[string]string{
				controllermeta.LabelWorkflow: workflow.Spec.WorkflowID,
				controllermeta.LabelStep:     step.Name},
		},
		Spec: v1alpha1.StepAttemptSpec{
			WorkflowRef:     v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID},
			StepName:        step.Name,
			RetryNumber:     number,
			Kind:            step.Kind,
			WorkflowAttempt: workflow.Status.WorkflowAttempt,
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
	executionRef, err := r.ensureDomainExecution(ctx, workflow, attempt, step, feedback)
	if err != nil {
		return err
	}
	if err := r.recordWorkflowRetry(ctx, workflow, attempt, executionRef); err != nil {
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

func (r *WorkflowReconciler) ensureDomainExecution(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, attempt *v1alpha1.StepAttempt, step v1alpha1.StepConfig, feedback *v1alpha1.FailedAgentAttempt) (*v1alpha1.TypedLocalReference, error) {
	inputs, err := r.resolveStepInputs(ctx, workflow, attempt, step)
	if err != nil {
		return nil, err
	}

	metadata := metav1.ObjectMeta{
		Name:      attempt.Name,
		Namespace: attempt.Namespace,
		Labels: map[string]string{
			controllermeta.LabelWorkflow: attempt.Labels[controllermeta.LabelWorkflow],
			controllermeta.LabelStep:     step.Name},
	}
	var object client.Object
	var reference v1alpha1.TypedLocalReference
	switch step.Kind {
	case v1alpha1.ExecutionKindAgent:
		priorTests, err := r.workflowRetryTestInput(ctx, workflow, step)
		if err != nil {
			return nil, err
		}
		if priorTests != nil {
			inputs = append(inputs, *priorTests)
		}
		if step.Agent == nil {
			return nil, fmt.Errorf("agent step %s has no agent specification", step.Name)
		}
		object = &v1alpha1.AgentRun{
			ObjectMeta: metadata,
			Spec: v1alpha1.AgentRunSpec{
				AttemptRef:      attempt.Name,
				WorkflowRef:     attempt.Spec.WorkflowRef,
				StepName:        step.Name,
				Attempt:         attempt.Spec.RetryNumber,
				PriorAttemptRef: copyFailedAgentAttempt(feedback),
				Responsibility:  step.Agent.Responsibility,
				Image:           step.Agent.Image,
				Executable:      append([]string(nil), step.Agent.Executable...),
				Capabilities:    append([]string(nil), step.Agent.Capabilities...),
				Inputs:          inputs,
				OutputContracts: append([]v1alpha1.ContractReference(nil), step.Outputs...),
				Inference:       copyInferenceRequest(step.Agent.Inference),
				Timeout:         step.Timeout,
			}}
		reference = v1alpha1.TypedLocalReference{
			APIVersion: v1alpha1.GroupVersion.String(),
			Kind:       "AgentRun",
			Name:       attempt.Name}
	case v1alpha1.ExecutionKindUtility:
		if step.Utility == nil {
			return nil, fmt.Errorf("utility step %s has no utility operation", step.Name)
		}
		approval, err := controllers.ResolveRequiredApproval(ctx, r.stepAttemptReader(), r.Audit, workflow, attempt, step, inputs)
		if err != nil {
			return nil, err
		}
		object = &v1alpha1.UtilityOperation{ObjectMeta: metadata, Spec: v1alpha1.UtilityOperationSpec{
			Approval:        approval,
			AttemptRef:      attempt.Name,
			WorkflowRef:     attempt.Spec.WorkflowRef,
			StepName:        step.Name,
			Attempt:         attempt.Spec.RetryNumber,
			Operation:       copyUtilityOperation(*step.Utility),
			Inputs:          inputs,
			OutputContracts: append([]v1alpha1.ContractReference(nil), step.Outputs...),
			Timeout:         step.Timeout,
		}}
		reference = v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "UtilityOperation", Name: attempt.Name}
	case v1alpha1.ExecutionKindHumanGate:
		if step.Approval == nil {
			return nil, fmt.Errorf("human gate %s has no approval specification", step.Name)
		}
		object = &v1alpha1.ApprovalRequest{
			ObjectMeta: metadata,
			Spec: v1alpha1.ApprovalRequestSpec{
				AttemptRef:  v1alpha1.UIDReference{Name: attempt.Name, UID: attempt.UID},
				WorkflowRef: attempt.Spec.WorkflowRef,
				StepName:    step.Name,
				Attempt:     attempt.Spec.RetryNumber,
				Approval:    copyApprovalSpec(*step.Approval),
				Inputs:      inputs,
			}}
		reference = v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ApprovalRequest", Name: attempt.Name}
	case v1alpha1.ExecutionKindValidation:
		if step.Validation == nil {
			return nil, fmt.Errorf("validation step %s has no validation specification", step.Name)
		}
		object = &v1alpha1.ValidationRun{
			ObjectMeta: metadata,
			Spec: v1alpha1.ValidationRunSpec{
				AttemptRef:  attempt.Name,
				WorkflowRef: attempt.Spec.WorkflowRef,
				StepName:    step.Name,
				Attempt:     attempt.Spec.RetryNumber,
				Provider:    step.Validation.Provider,
				Commit:      step.Validation.Commit,
				ImageDigest: step.Validation.ImageDigest,
				OverlayPath: step.Validation.OverlayPath,
				Destination: step.Validation.Destination,
				Inputs:      inputs,
			}}
		reference = v1alpha1.TypedLocalReference{
			APIVersion: v1alpha1.GroupVersion.String(),
			Kind:       "ValidationRun",
			Name:       attempt.Name}
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
	if attempt.Spec.Kind != v1alpha1.ExecutionKindAgent &&
		!(attempt.Spec.Kind == v1alpha1.ExecutionKindUtility && attempt.Status.FailureReason == utilitycontract.TestRunCodeError) {
		return nil
	}
	if attempt.Status.FailureReason == "" || attempt.Status.FailureMessage == "" {
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
		"retryAttempt":  attemptName(step.Name, workflow.Status.WorkflowAttempt, nextAttempt),
	}
	retryData := map[string]any{
		"retryFrom":        step.Name,
		"previousAttempt":  failedAttempt.Spec.RetryNumber,
		"nextAttempt":      nextAttempt,
		"failureReason":    failedAttempt.Status.FailureReason,
		"interruptedPhase": failedAttempt.Status.Phase,
	}
	payload, err := r.recoveryDecision(ctx, workflow, failedAttempt, step, nextAttempt)
	if err != nil {
		return err
	}
	if err := r.appendWorkflowEvent(ctx, workflow, "RecoveryDecisionSelected", step.Name, failedAttempt.Spec.RetryNumber, "select", step.Name, "selected", failedAttempt.Status.FailureReason, references, payload); err != nil {
		return err
	}
	return r.appendWorkflowEvent(ctx, workflow, "StepAttemptRetried", step.Name, nextAttempt, "retry", attemptName(step.Name, workflow.Status.WorkflowAttempt, nextAttempt), "created", failedAttempt.Status.FailureReason, references, retryData)
}

func (r *WorkflowReconciler) recoveryDecision(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, failedAttempt *v1alpha1.StepAttempt, step v1alpha1.StepConfig, nextAttempt int32) (audit.DecisionEvaluated, error) {
	decisionInput, err := json.Marshal(struct {
		WorkflowAttempt int32                      `json:"workflowAttempt"`
		Step            v1alpha1.StepConfig        `json:"step"`
		FailedAttempt   v1alpha1.StepAttemptStatus `json:"failedAttemptStatus"`
		FailedRef       string                     `json:"failedAttemptRef"`
		NextAttempt     int32                      `json:"nextAttempt"`
	}{workflow.Status.WorkflowAttempt, step, failedAttempt.Status, failedAttempt.Name, nextAttempt})
	if err != nil {
		return audit.DecisionEvaluated{}, fmt.Errorf("marshal recovery decision input: %w", err)
	}
	inputDigest := artifactcontract.DigestBytes(decisionInput)
	evidenceEvents, err := r.failedAttemptEvidenceEvents(ctx, workflow, failedAttempt)
	if err != nil {
		return audit.DecisionEvaluated{}, err
	}
	maxAttempts := step.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 1
	}
	return audit.DecisionEvaluated{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Primitive:     workflowResourceRef("SovereignWorkflow", workflow),
		Decision: audit.DecisionRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			ID:            audit.DeterministicID("workflow-recovery-selection", string(workflow.UID), failedAttempt.Name, inputDigest),
			Kind:          "controller",
			Revision:      "workflow-recovery/v1",
			InputDigest:   inputDigest,
			Outcome:       "selected",
		},
		EvidenceEvents: evidenceEvents,
		Invariants: []audit.InvariantResult{
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "failed-attempt-terminal", Outcome: "passed", Expected: "Failed or Interrupted", Observed: string(failedAttempt.Status.Phase), Reason: "recovery is evaluated only for a terminal unsuccessful attempt"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "failed-attempt-retryable", Outcome: "passed", Expected: "true", Observed: fmt.Sprint(failedAttempt.Status.Retryable), Reason: "the execution primitive classified the failure as retryable"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "step-attempt-budget-remaining", Outcome: "passed", Expected: fmt.Sprintf("next attempt <= %d", maxAttempts), Observed: fmt.Sprint(nextAttempt), Reason: "the selected retry remains within the declared step policy"},
		},
	}, nil
}

func (r *WorkflowReconciler) failedAttemptEvidenceEvents(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, failedAttempt *v1alpha1.StepAttempt) ([]string, error) {
	if r.Audit == nil {
		return nil, nil
	}
	events, err := r.Audit.ListWorkflow(ctx, workflow.Name)
	if err != nil {
		return nil, err
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if (event.Type == "StepAttemptFailed" || event.Type == "StepAttemptInterrupted") &&
			event.Target == failedAttempt.Name && event.Subject.Namespace == failedAttempt.Namespace &&
			event.Subject.Step == failedAttempt.Spec.StepName && event.Subject.Attempt == failedAttempt.Spec.RetryNumber {
			return []string{event.ID}, nil
		}
	}
	return nil, nil
}

func (r *WorkflowReconciler) appendWorkflowCompleted(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, finalAttempt *v1alpha1.StepAttempt) error {
	if r.Audit == nil {
		return nil
	}
	var err error
	if finalAttempt == nil {
		finalAttempt, err = r.findFinalSucceededAttempt(ctx, workflow)
		if err != nil {
			return err
		}
	}
	decisionEvent, err := r.workflowCompletionDecisionEvent(ctx, workflow, finalAttempt)
	if err != nil {
		return err
	}
	workflowResRef := workflowResourceRef("SovereignWorkflow", workflow)
	consequences := []audit.ConsequenceEvidence{{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Kind:          "workflow-state",
		Resource:      &workflowResRef,
		After:         string(v1alpha1.PhaseSucceeded),
	}}
	if finalAttempt != nil {
		attemptRef := workflowResourceRef("StepAttempt", finalAttempt)
		consequences = append(consequences, audit.ConsequenceEvidence{SchemaVersion: audit.PayloadSchemaVersionV1, Kind: "final-step-attempt", Resource: &attemptRef, After: string(finalAttempt.Status.Phase)})
		var artifactList v1alpha1.ArtifactList
		if err := r.Client.List(ctx, &artifactList, client.InNamespace(workflow.Namespace)); err != nil {
			return err
		}
		sort.Slice(artifactList.Items, func(i, j int) bool { return artifactList.Items[i].Name < artifactList.Items[j].Name })
		producerName := finalAttempt.Name
		if finalAttempt.Status.ExecutionRef != nil {
			producerName = finalAttempt.Status.ExecutionRef.Name
		}
		for index := range artifactList.Items {
			artifact := &artifactList.Items[index]
			if artifact.Spec.WorkflowRef != finalAttempt.Spec.WorkflowRef || artifact.Spec.ProducerRef.Name != producerName || !artifacts.ArtifactAccepted(artifact) {
				continue
			}
			evidence := controllers.ArtifactEvidence(artifact)
			consequences = append(consequences, audit.ConsequenceEvidence{SchemaVersion: audit.PayloadSchemaVersionV1, Kind: "final-artifact", Resource: &evidence.Artifact, Artifact: &evidence, Digest: artifact.Spec.Digest, Target: evidence.Contract})
		}
	}
	payload := audit.ConsequenceRecorded{SchemaVersion: audit.PayloadSchemaVersionV1, DecisionEvent: decisionEvent, Consequences: consequences}
	stepName := ""
	attemptNumber := int32(0)
	if finalAttempt != nil {
		stepName = finalAttempt.Spec.StepName
		attemptNumber = finalAttempt.Spec.RetryNumber
	}
	return r.appendWorkflowEvent(ctx, workflow, "WorkflowCompleted", stepName, attemptNumber, "complete", workflow.Name, "succeeded", "AllStepsSucceeded", map[string]string{"finalDecisionEvent": decisionEvent}, payload)
}

func (r *WorkflowReconciler) findFinalSucceededAttempt(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) (*v1alpha1.StepAttempt, error) {
	if len(workflow.Spec.Steps) == 0 {
		return nil, nil
	}
	finalStep := workflow.Spec.Steps[len(workflow.Spec.Steps)-1].Name
	var attempts v1alpha1.StepAttemptList
	if err := r.stepAttemptReader().List(ctx, &attempts, client.InNamespace(workflow.Namespace), client.MatchingLabels{controllermeta.LabelWorkflow: workflow.Spec.WorkflowID, controllermeta.LabelStep: finalStep}); err != nil {
		return nil, err
	}
	var selected *v1alpha1.StepAttempt
	for index := range attempts.Items {
		attempt := &attempts.Items[index]
		if attempt.Status.Phase != v1alpha1.PhaseSucceeded {
			continue
		}
		if selected == nil || attempt.Spec.WorkflowAttempt > selected.Spec.WorkflowAttempt || (attempt.Spec.WorkflowAttempt == selected.Spec.WorkflowAttempt && attempt.Spec.RetryNumber > selected.Spec.RetryNumber) {
			selected = attempt
		}
	}
	return selected, nil
}

func (r *WorkflowReconciler) workflowCompletionDecisionEvent(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, attempt *v1alpha1.StepAttempt) (string, error) {
	if r.Audit == nil || attempt == nil || attempt.Status.ExecutionRef == nil {
		return "", nil
	}
	wantedType := map[string]string{"AgentRun": "AgentExecutionAuthorized", "UtilityOperation": "UtilityExecutionAuthorized", "ValidationRun": "ValidationEvaluated"}[attempt.Status.ExecutionRef.Kind]
	events, err := r.Audit.ListWorkflow(ctx, workflow.Name)
	if err != nil {
		return "", err
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type == wantedType && (event.Target == attempt.Status.ExecutionRef.Name || event.References["agentRun"] == attempt.Status.ExecutionRef.Name || event.References["utilityOperation"] == attempt.Status.ExecutionRef.Name || event.References["validationRun"] == attempt.Status.ExecutionRef.Name) {
			return event.ID, nil
		}
	}
	return "", nil
}

func workflowResourceRef(kind string, object client.Object) audit.ResourceRef {
	return audit.ResourceRef{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		APIVersion:    v1alpha1.GroupVersion.String(),
		Kind:          kind,
		Namespace:     object.GetNamespace(),
		Name:          object.GetName(),
		UID:           string(object.GetUID())}
}

func (r *WorkflowReconciler) appendWorkflowEvent(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, eventType, step string, attempt int32, action, target, outcome, reason string, references map[string]string, data any) error {
	decisionID := ""
	switch payload := data.(type) {
	case audit.DecisionEvaluated:
		decisionID = payload.Decision.ID
	case *audit.DecisionEvaluated:
		if payload != nil {
			decisionID = payload.Decision.ID
		}
	}
	return audit.AppendControllerEvent(ctx, r.Audit, "workflow-controller", r.Now, audit.EventOptions{
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
		DecisionID:    decisionID,
		References:    references,
		Data:          data,
	})
}

func attemptName(step string, workflowAttempt, retryNumber int32) string {
	clean := strings.Trim(strings.ToLower(step), "-")
	clean = strings.ReplaceAll(clean, "_", "-")

	if len(clean) > 45 {
		clean = clean[:45]
	}

	return fmt.Sprintf(
		"%s-w%03d-r%03d",
		clean,
		workflowAttempt,
		retryNumber,
	)
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

func workspaceWriterLeaseName(workflowName string) string {
	const suffix = "-workspace-writer"
	maximumPrefix := 63 - len(suffix)
	prefix := strings.Trim(workflowName, "-")
	if len(prefix) > maximumPrefix {
		prefix = strings.TrimRight(prefix[:maximumPrefix], "-")
	}
	return prefix + suffix
}
