package validationrun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/validation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const ValidationFinalizer = "sovereign-ai.io/validation-cleanup"

type ValidationRunReconciler struct {
	client.Client
	Provider validation.Provider
	Audit    audit.Recorder
	Now      func() time.Time
}

func (r *ValidationRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.ValidationRun{}).Complete(r)
}

func (r *ValidationRunReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var run v1alpha1.ValidationRun
	if err := r.Get(ctx, request.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check for deletion
	if !run.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&run, ValidationFinalizer) {
			reference := run.Status.ProviderRef
			if reference == "" {
				reference = validation.ApplicationName(run.Namespace, run.Name, string(run.UID))
			}
			if err := r.Provider.Destroy(ctx, reference); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			// Argo's resources finalizer determines when the preview is gone.
			// Keep this finalizer until it is actually gone
			if _, err := r.Provider.Status(ctx, reference); !apierrors.IsNotFound(err) {
				if err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			controllerutil.RemoveFinalizer(&run, ValidationFinalizer)
			return ctrl.Result{}, r.Update(ctx, &run)
		}
		return ctrl.Result{}, nil
	}

	// Check for namespace termination
	terminating, err := controllers.NamespaceTerminating(ctx, r.Client, run.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if terminating {
		return ctrl.Result{}, nil
	}
	if run.Status.Phase == v1alpha1.PhaseSucceeded || run.Status.Phase == v1alpha1.PhaseFailed {
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&run, ValidationFinalizer) {
		controllerutil.AddFinalizer(&run, ValidationFinalizer)
		return ctrl.Result{}, r.Update(ctx, &run)
	}
	authorized, err := controllers.ValidateDomainAuthority(ctx, r.Client, &run, run.Spec.AttemptRef, v1alpha1.ExecutionKindValidation, run.Spec.WorkflowRef, run.Spec.StepName, run.Spec.Attempt)
	if err != nil {
		run.Status.Phase = v1alpha1.PhaseFailed
		run.Status.FailureReason = "InvalidStepAttemptAuthority"
		run.Status.Retryable = false
		run.Status.ObservedGeneration = run.Generation
		return ctrl.Result{}, r.Status().Update(ctx, &run)
	}
	if !authorized {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	if run.Status.ProviderRef == "" {
		project, err := r.validationProject(ctx, &run)
		if err != nil {
			return ctrl.Result{}, err
		}
		ready := v1alpha1.ProjectReady(project)
		condition := metav1.Condition{Type: v1alpha1.ProjectConditionValidationProviderReady, Status: metav1.ConditionFalse, Reason: "ProviderNotReady", Message: "Waiting for the project's current configuration and Argo AppProject policy", ObservedGeneration: run.Generation}
		if ready {
			condition.Status, condition.Reason, condition.Message = metav1.ConditionTrue, "AppProjectReady", "Project validation policy is ready"
		}
		if apiMeta.SetStatusCondition(&run.Status.Conditions, condition) {
			if err := r.Status().Update(ctx, &run); err != nil {
				return ctrl.Result{}, err
			}
		}
		if !ready {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		request, resolution, message, err := r.providerRequest(ctx, &run)
		if err != nil {
			return ctrl.Result{}, err
		}
		switch resolution {
		case "pending":
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		case "invalid":
			return ctrl.Result{}, r.failInvalidInputs(ctx, &run, message)
		case "ready":
			// Only ready input evidence authorizes a deployment.
		default:
			return ctrl.Result{}, fmt.Errorf("unexpected validation input resolution %q", resolution)
		}
		if r.Audit != nil {
			authorityEventID, err := r.appendExecutionAuthorityEstablished(ctx, &run, project)
			if err != nil {
				return ctrl.Result{}, err
			}
			evidence, evidenceReady, invalidReason, err := controllers.ResolveArtifactEvidence(
				ctx,
				r.Client,
				run.Namespace,
				run.Spec.WorkflowRef,
				run.Spec.Inputs,
			)
			if err != nil {
				return ctrl.Result{}, err
			}
			if invalidReason != "" {
				return ctrl.Result{}, r.failInvalidInputs(ctx, &run, invalidReason)
			}
			if !evidenceReady {
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			inputEventID, err := r.appendInputsResolved(ctx, &run, evidence)
			if err != nil {
				return ctrl.Result{}, err
			}
			_ = authorityEventID
			_ = inputEventID
		}
		reference, err := r.Provider.Start(ctx, request)
		if err != nil {
			return ctrl.Result{}, err
		}
		run.Status.ProviderRef = reference
		run.Status.Phase = v1alpha1.PhaseValidating
		run.Status.ObservedGeneration = run.Generation
		if err := r.Status().Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.appendValidationEvent(ctx, &run, "ValidationRunCreated", "create", "created", "")
	}
	status, err := r.Provider.Status(ctx, run.Status.ProviderRef)
	if err != nil {
		return ctrl.Result{}, err
	}
	run.Status.AccessURL = status.AccessURL
	if status.Ready {
		run.Status.Phase = v1alpha1.PhaseSucceeded
	} else if status.Failed {
		run.Status.Phase = v1alpha1.PhaseFailed
	} else {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if err := r.appendValidationEvaluated(ctx, &run, status); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Status().Update(ctx, &run); err != nil {
		return ctrl.Result{}, err
	}
	eventType := "ValidationRunReady"
	outcome := "ready"
	if run.Status.Phase == v1alpha1.PhaseFailed {
		eventType = "ValidationRunFailed"
		outcome = "failed"
	}
	return ctrl.Result{}, r.appendValidationEvent(ctx, &run, eventType, "observe", outcome, string(run.Status.Phase))
}

func (r *ValidationRunReconciler) appendValidationEvent(ctx context.Context, run *v1alpha1.ValidationRun, eventType, action, outcome, reason string) error {
	return r.appendValidationEventWithData(ctx, run, eventType, action, outcome, reason, map[string]any{
		"provider":    run.Spec.Provider,
		"overlayPath": run.Spec.OverlayPath,
		"destination": run.Spec.Destination,
	})
}

func (r *ValidationRunReconciler) appendValidationEventWithData(ctx context.Context, run *v1alpha1.ValidationRun, eventType, action, outcome, reason string, data any) error {
	_, err := r.appendValidationEventWithDataResult(ctx, run, eventType, action, outcome, reason, data)
	return err
}

func (r *ValidationRunReconciler) appendValidationEventWithDataResult(ctx context.Context, run *v1alpha1.ValidationRun, eventType, action, outcome, reason string, data any) (audit.Event, error) {
	decisionID := ""
	if payload, ok := data.(audit.DecisionEvaluated); ok {
		decisionID = payload.Decision.ID
	}
	return audit.BuildAndAppendControllerEvent(ctx, r.Audit, "validationrun-controller", r.Now, audit.EventOptions{
		Type: eventType,
		Subject: audit.Subject{
			Namespace: run.Namespace,
			Workflow:  run.Spec.WorkflowRef.Name,
			Step:      run.Spec.StepName,
			Attempt:   run.Spec.Attempt,
		},
		Action:     action,
		Target:     run.Name,
		Outcome:    outcome,
		Reason:     reason,
		DecisionID: decisionID,
		References: map[string]string{
			"validationRun": run.Name,
			"providerRef":   run.Status.ProviderRef,
			"validationURL": run.Status.AccessURL,
			"commit":        run.Spec.Commit,
			"imageDigest":   run.Spec.ImageDigest,
		},
		Data: data,
	})
}

func (r *ValidationRunReconciler) appendInputsResolved(ctx context.Context, run *v1alpha1.ValidationRun, evidence []audit.ArtifactEvidence) (string, error) {
	payload := controllers.InputsResolvedPayload("ValidationRun", run, evidence)
	event, err := r.appendValidationEventWithDataResult(ctx, run, "InputsResolved", "resolve-inputs", "resolved", "", payload)
	if err != nil {
		return "", err
	}
	return event.ID, nil
}

func (r *ValidationRunReconciler) appendExecutionAuthorityEstablished(ctx context.Context, run *v1alpha1.ValidationRun, project *v1alpha1.SovereignProject) (string, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return "", fmt.Errorf("read workflow for validation authority evidence: %w", err)
	}
	var attempt v1alpha1.StepAttempt
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.AttemptRef}, &attempt); err != nil {
		return "", fmt.Errorf("read step attempt for validation authority evidence: %w", err)
	}
	owner := metav1.GetControllerOf(run)
	if workflow.UID == "" || attempt.UID == "" || run.UID == "" {
		return "", fmt.Errorf("validation authority evidence requires workflow, step attempt, and primitive UIDs")
	}
	if workflow.UID != run.Spec.WorkflowRef.UID || owner == nil || owner.UID != attempt.UID {
		return "", fmt.Errorf("validation authority evidence does not match workflow and step-attempt ownership")
	}
	payload := audit.AuthorityEstablished{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Workflow:      validationResourceRef("SovereignWorkflow", &workflow),
		StepAttempt:   validationResourceRef("StepAttempt", &attempt),
		Primitive:     validationResourceRef("ValidationRun", run),
		Invariants: []audit.InvariantResult{
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "workflow-uid-bound", Outcome: "passed", Expected: string(workflow.UID), Observed: string(run.Spec.WorkflowRef.UID), Reason: "the validation primitive is bound to the admitted workflow"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "step-attempt-owns-primitive", Outcome: "passed", Expected: string(attempt.UID), Observed: string(owner.UID), Reason: "the authorized step attempt controls the validation primitive"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "validation-provider-authority-ready", Outcome: "passed", Expected: project.Status.ValidationProviderRef, Observed: project.Status.ValidationProviderRef, Reason: "the project-owned validation provider policy was ready before deployment"},
		},
	}
	event, err := r.appendValidationEventWithDataResult(ctx, run, "ExecutionAuthorityEstablished", "authorize", "established", "", payload)
	if err != nil {
		return "", err
	}
	return event.ID, nil
}

func validationResourceRef(kind string, object client.Object) audit.ResourceRef {
	return audit.ResourceRef{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		APIVersion:    v1alpha1.GroupVersion.String(),
		Kind:          kind,
		Namespace:     object.GetNamespace(),
		Name:          object.GetName(),
		UID:           string(object.GetUID()),
	}
}

func (r *ValidationRunReconciler) appendValidationEvaluated(ctx context.Context, run *v1alpha1.ValidationRun, status validation.Status) error {
	authorityEventID, err := r.findValidationEventID(ctx, run, "ExecutionAuthorityEstablished")
	if err != nil {
		return err
	}
	inputEventID, err := r.findValidationEventID(ctx, run, "InputsResolved")
	if err != nil {
		return err
	}
	decisionInput, err := json.Marshal(struct {
		RunSpec     v1alpha1.ValidationRunSpec `json:"runSpec"`
		ProviderRef string                     `json:"providerRef"`
		Status      validation.Status          `json:"status"`
	}{run.Spec, run.Status.ProviderRef, status})
	if err != nil {
		return fmt.Errorf("marshal validation evaluation input: %w", err)
	}
	inputDigest := artifactcontract.DigestBytes(decisionInput)
	outcome := "failed"
	decisionOutcome := "denied"
	if status.Ready {
		outcome = "passed"
		decisionOutcome = "allowed"
	}
	syncStatus := status.SyncStatus
	if syncStatus == "" && status.Ready {
		syncStatus = "Synced"
	}
	healthStatus := status.HealthStatus
	if healthStatus == "" {
		healthStatus = status.Phase
	}
	payload := audit.DecisionEvaluated{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Primitive:     validationResourceRef("ValidationRun", run),
		Decision: audit.DecisionRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			ID:            audit.DeterministicID("validation-evaluation", string(run.UID), inputDigest),
			Kind:          "controller",
			Revision:      "validation-provider/v1",
			InputDigest:   inputDigest,
			Outcome:       decisionOutcome,
		},
		AuthorityEvent: authorityEventID,
		InputEvent:     inputEventID,
		Invariants: []audit.InvariantResult{
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "validation-authority-linked", Outcome: "passed", Expected: authorityEventID, Observed: authorityEventID, Reason: "workflow and provider authority were established before validation"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "validation-inputs-linked", Outcome: "passed", Expected: inputEventID, Observed: inputEventID, Reason: "the exact candidate, remote proof, and image evidence were resolved before validation"},
			validationInvariant("validation-provider-synced", "Synced", syncStatus),
			validationInvariant("validation-provider-healthy", "Healthy", healthStatus),
			validationInvariant("validation-provider-ready", "true", fmt.Sprint(status.Ready)),
		},
	}
	_, err = r.appendValidationEventWithDataResult(ctx, run, "ValidationEvaluated", "evaluate", outcome, status.Message, payload)
	return err
}

func validationInvariant(id, expected, observed string) audit.InvariantResult {
	outcome := "failed"
	if expected == observed {
		outcome = "passed"
	}
	return audit.InvariantResult{SchemaVersion: audit.PayloadSchemaVersionV1, ID: id, Outcome: outcome, Expected: expected, Observed: observed}
}

func (r *ValidationRunReconciler) findValidationEventID(ctx context.Context, run *v1alpha1.ValidationRun, eventType string) (string, error) {
	if r.Audit == nil {
		return "", nil
	}
	events, err := r.Audit.ListWorkflow(ctx, run.Spec.WorkflowRef.Name)
	if err != nil {
		return "", err
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type == eventType && event.References["validationRun"] == run.Name {
			return event.ID, nil
		}
	}
	return "", fmt.Errorf("required %s event for ValidationRun %s was not recorded", eventType, run.Name)
}

// providerRequest returns a ready, pending, or invalid resolution with an
// explanatory message. Only operational failures are returned as errors.
func (r *ValidationRunReconciler) providerRequest(ctx context.Context, run *v1alpha1.ValidationRun) (request validation.Request, resolution, message string, err error) {
	project, err := r.validationProject(ctx, run)
	if err != nil {
		return validation.Request{}, "", "", err
	}
	if !v1alpha1.ProjectReady(project) {
		return validation.Request{}, "pending", "project validation provider is not ready", nil
	}
	subject, resolution, message, err := r.resolveValidationSubject(ctx, run, project)
	if err != nil || resolution != "ready" {
		return validation.Request{}, resolution, message, err
	}
	return validation.Request{
		Name: validation.ApplicationName(run.Namespace, run.Name, string(run.UID)), WorkflowNamespace: run.Namespace, Project: project.Status.ValidationProviderRef,
		InfrastructureRepo: project.Spec.Validation.InfrastructureRepo, InfrastructureRevision: subject.commit,
		OverlayPath: project.Spec.Validation.OverlayPath, ImageName: project.Spec.Validation.ImageName,
		ImageDigest: subject.imageReference, Commit: subject.commit,
	}, "ready", "", nil
}

func (r *ValidationRunReconciler) validationProject(ctx context.Context, run *v1alpha1.ValidationRun) (*v1alpha1.SovereignProject, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return nil, err
	}
	var project v1alpha1.SovereignProject
	if err := r.Get(ctx, types.NamespacedName{Name: workflow.Spec.Project.Name}, &project); err != nil {
		return nil, err
	}
	if workflow.Spec.Project.UID != "" && workflow.Spec.Project.UID != project.UID {
		return nil, fmt.Errorf("workflow references a different SovereignProject UID")
	}
	return &project, nil
}

type resolvedValidationSubject struct {
	commit         string
	tree           string
	imageReference string
}

func (r *ValidationRunReconciler) resolveValidationSubject(
	ctx context.Context,
	run *v1alpha1.ValidationRun,
	project *v1alpha1.SovereignProject,
) (subject resolvedValidationSubject, resolution, message string, err error) {
	candidateArtifact, resolution, message, err := r.resolveValidationArtifact(ctx, run, artifactcontract.CandidateRevisionContract)
	if err != nil || resolution != "ready" {
		return resolvedValidationSubject{}, resolution, message, err
	}
	remoteProofArtifact, resolution, message, err := r.resolveValidationArtifact(ctx, run, artifactcontract.CandidateRemoteProofContract)
	if err != nil || resolution != "ready" {
		return resolvedValidationSubject{}, resolution, message, err
	}
	imageArtifact, resolution, message, err := r.resolveValidationArtifact(ctx, run, artifactcontract.ImageDigestContract)
	if err != nil || resolution != "ready" {
		return resolvedValidationSubject{}, resolution, message, err
	}

	candidate := candidateArtifact.Spec.Claims.CandidateRevision
	remoteProof := remoteProofArtifact.Spec.Claims.CandidateRemoteProof
	image := imageArtifact.Spec.Claims.ImageDigest

	if remoteProof.CandidateRevisionDigest != candidateArtifact.Spec.Digest {
		return resolvedValidationSubject{}, "invalid", "candidate remote proof does not reference the selected candidate revision", nil
	}
	if image.CandidateRevisionDigest != candidateArtifact.Spec.Digest {
		return resolvedValidationSubject{}, "invalid", "image digest does not reference the selected candidate revision", nil
	}
	if remoteProof.ObservedCommit != candidate.Commit {
		return resolvedValidationSubject{}, "invalid", "candidate remote proof observed a different commit", nil
	}
	if image.CandidateCommit != candidate.Commit || image.CandidateTree != candidate.Tree {
		return resolvedValidationSubject{}, "invalid", "image digest candidate commit or tree does not match the selected candidate revision", nil
	}
	if remoteProof.RepositoryURL != candidate.RepositoryURL {
		return resolvedValidationSubject{}, "invalid", "candidate remote proof repository does not match the selected candidate revision", nil
	}
	if candidate.RepositoryURL != project.Spec.ApplicationRepository.URL {
		return resolvedValidationSubject{}, "invalid", "candidate revision repository does not match the SovereignProject application repository", nil
	}
	if project.Spec.Validation.InfrastructureRepo != candidate.RepositoryURL {
		return resolvedValidationSubject{}, "invalid", "MVP validation requires the infrastructure and candidate repositories to be the same", nil
	}
	if image.ImageRepository != project.Spec.Validation.ImageName {
		return resolvedValidationSubject{}, "invalid", "built image repository does not match the SovereignProject validation image name", nil
	}

	imageReference := image.ImageRepository + "@" + image.OCIDigest
	if run.Spec.Commit != "" && run.Spec.Commit != candidate.Commit {
		return resolvedValidationSubject{}, "invalid", "validation step commit constraint does not match artifact evidence", nil
	}
	if run.Spec.ImageDigest != "" && run.Spec.ImageDigest != imageReference {
		return resolvedValidationSubject{}, "invalid", "validation step image constraint does not match artifact evidence", nil
	}

	return resolvedValidationSubject{
		commit:         candidate.Commit,
		tree:           candidate.Tree,
		imageReference: imageReference,
	}, "ready", "", nil
}

func (r *ValidationRunReconciler) resolveValidationArtifact(
	ctx context.Context,
	run *v1alpha1.ValidationRun,
	contract string,
) (resolved *v1alpha1.Artifact, resolution, message string, err error) {
	name, version, _ := strings.Cut(contract, "/")
	var selected *v1alpha1.ArtifactReference
	for index := range run.Spec.Inputs {
		input := &run.Spec.Inputs[index]
		if input.Name != name {
			continue
		}
		if selected != nil {
			return nil, "invalid", fmt.Sprintf("validation input %q is declared more than once", name), nil
		}
		selected = input
	}
	if selected == nil || selected.ArtifactRef == nil || selected.Digest == "" {
		return nil, "invalid", fmt.Sprintf("validation input %q is not pinned to an immutable Artifact", name), nil
	}

	artifact, ready, invalidReason, err := artifacts.ResolvePinnedInput(
		ctx,
		r.Client,
		run.Namespace,
		run.Spec.WorkflowRef,
		*selected,
	)
	if err != nil {
		return nil, "", "", err
	}
	if invalidReason != "" {
		return nil, "invalid", invalidReason, nil
	}
	if !ready {
		return nil, "pending", fmt.Sprintf("validation input %q is not accepted yet", name), nil
	}
	if artifact.Spec.Contract.Version != version {
		return nil, "invalid", fmt.Sprintf("validation input %q uses contract version %q, expected %q", name, artifact.Spec.Contract.Version, version), nil
	}
	if err := artifacts.ValidateClaims(artifact.Spec.Contract, artifact.Spec.Claims); err != nil {
		return nil, "invalid", fmt.Sprintf("validation input %q has invalid claims: %v", name, err), nil
	}
	return artifact, "ready", "", nil
}

func (r *ValidationRunReconciler) failInvalidInputs(ctx context.Context, run *v1alpha1.ValidationRun, message string) error {
	run.Status.Phase = v1alpha1.PhaseFailed
	run.Status.FailureReason = "InvalidValidationInputs"
	run.Status.Retryable = false
	run.Status.ObservedGeneration = run.Generation
	apiMeta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
		Type:               "InputsResolved",
		Status:             metav1.ConditionFalse,
		Reason:             "InvalidValidationInputs",
		Message:            message,
		ObservedGeneration: run.Generation,
	})
	if err := r.Status().Update(ctx, run); err != nil {
		return err
	}
	return r.appendValidationEvent(ctx, run, "ValidationRunFailed", "resolve-inputs", "failed", "InvalidValidationInputs")
}
