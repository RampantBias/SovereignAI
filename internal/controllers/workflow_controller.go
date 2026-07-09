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
			controllerutil.RemoveFinalizer(&workflow, WorkflowFinalizer)
			return ctrl.Result{}, r.Update(ctx, &workflow)
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(&workflow, WorkflowFinalizer) {
		controllerutil.AddFinalizer(&workflow, WorkflowFinalizer)
		return ctrl.Result{}, r.Update(ctx, &workflow)
	}

	// Ensure workflow has steps
	if len(workflow.Spec.Steps) == 0 {
		return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "InvalidDefinition", "workflow must contain at least one step")
	}

	// Trigger new workflow into pending
	if workflow.Status.Phase == "" {
		workflow.Status.Phase = string(v1alpha1.PhasePending)
		workflow.Status.ObservedGeneration = workflow.Generation
		return ctrl.Result{}, r.Status().Update(ctx, &workflow)
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
			workflow.Status.Phase = string(v1alpha1.PhaseSucceeded)
			workflow.Status.ActiveStepName = ""
			workflow.Status.ActiveAttemptRef = ""
			workflow.Status.ObservedGeneration = workflow.Generation
			return ctrl.Result{}, r.Status().Update(ctx, &workflow)
		}
		return ctrl.Result{}, r.createAttempt(ctx, &workflow, next, 1)
	// On PhaseFailed or PhaseInterrupted, we need to evaluate
	case v1alpha1.PhaseFailed, v1alpha1.PhaseInterrupted:
		step, found := findStep(workflow.Spec.Steps, attempt.Spec.StepName)
		if !found {
			return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "StepMissing", attempt.Spec.StepName)
		}
		maxAttempts := step.MaxAttempts
		if maxAttempts == 0 {
			maxAttempts = 1
		}
		if attempt.Status.Retryable && attempt.Spec.Attempt < maxAttempts {
			return ctrl.Result{}, r.createAttempt(ctx, &workflow, step, attempt.Spec.Attempt+1)
		}
		return ctrl.Result{}, r.failWorkflow(ctx, &workflow, "StepFailed", attempt.Status.FailureReason)
	}
	return ctrl.Result{}, nil
}

func (r *WorkflowReconciler) ensureWorkspace(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) error {
	name := workflow.Name + "-workspace"
	var existing corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Namespace: workflow.Namespace, Name: name}, &existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if apierrors.IsNotFound(err) {
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
		if r.StorageClass != "" {
			claim.Spec.StorageClassName = &r.StorageClass
		}
		if err := controllerutil.SetControllerReference(workflow, claim, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, claim); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	workflow.Status.PvcName = name
	return r.Status().Update(ctx, workflow)
}

// CreateAttempt generates a new StepAttempt CRD to
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
			Goal:            step.Goal,
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
	workflow.Status.Phase = string(v1alpha1.PhaseRunning)
	workflow.Status.ActiveStepName = step.Name
	workflow.Status.ActiveAttemptRef = name
	workflow.Status.ObservedGeneration = workflow.Generation
	if err := r.Status().Update(ctx, workflow); err != nil {
		return err
	}
	if r.Audit != nil {
		now := time.Now().UTC()
		if r.Now != nil {
			now = r.Now().UTC()
		}
		event := audit.Event{
			ID:   audit.DeterministicID(string(workflow.UID), step.Name, fmt.Sprint(number), "created"),
			Type: "StepAttemptCreated", SchemaVersion: "v1", OccurredAt: now,
			Actor:   audit.Actor{Kind: "Controller", ID: "workflow-controller"},
			Subject: audit.Subject{Project: workflow.Spec.ProjectName, Namespace: workflow.Namespace, Workflow: workflow.Name, Step: step.Name, Attempt: number},
			Action:  "create", Target: name, Outcome: "created", CorrelationID: workflow.Name,
		}
		if err := r.Audit.Append(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

// Update Workflow status to failed
func (r *WorkflowReconciler) failWorkflow(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, reason, message string) error {
	workflow.Status.Phase = string(v1alpha1.PhaseFailed)
	workflow.Status.Conditions = []metav1.Condition{{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: message,
		ObservedGeneration: workflow.Generation, LastTransitionTime: metav1.Now(),
	}}
	return r.Status().Update(ctx, workflow)
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
