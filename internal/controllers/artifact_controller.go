package controllers

import (
	"context"
	"reflect"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	batchv1 "k8s.io/api/batch/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// / Artifact Controller is responsible for validating artifact metadata/digest
type ArtifactReconciler struct {
	client.Client
	Audit     audit.Recorder
	Contracts *artifactcontract.Registry
}

func (r *ArtifactReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.Artifact{}).Complete(r)
}

func (r *ArtifactReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var artifact v1alpha1.Artifact
	if err := r.Get(ctx, request.NamespacedName, &artifact); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Check for namespace termination
	terminating, err := NamespaceTerminating(ctx, r.Client, artifact.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if terminating {
		return ctrl.Result{}, nil
	}
	// Bootstrap ingress is intentionally deleted after acceptance. Its
	// immutable Artifact spec and terminal status remain sufficient on later
	// reconciliations; consumption performs its own identity/digest checks.
	if artifact.Spec.ProducerRef.Kind == "SovereignWorkflow" &&
		artifacts.ArtifactAccepted(&artifact) && artifacts.ValidateClaims(artifact.Spec.Contract, artifact.Spec.Claims) == nil {
		return ctrl.Result{}, r.appendArtifactEvent(ctx, &artifact, v1alpha1.PhaseSucceeded, "ContractAccepted")
	}

	phase := v1alpha1.PhaseSucceeded
	condition := metav1.Condition{
		Type:               "Valid",
		Status:             metav1.ConditionTrue,
		Reason:             "ContractAccepted",
		Message:            "stored bytes, digest, and typed contract are valid",
		ObservedGeneration: artifact.Generation,
	}
	reason, message := r.validateStoredArtifact(ctx, &artifact)
	if reason != "" {
		phase = v1alpha1.PhaseFailed
		condition.Status = metav1.ConditionFalse
		condition.Reason = reason
		condition.Message = message
	}

	if artifact.Status.Phase == phase && artifact.Status.ObservedGeneration == artifact.Generation {
		if phase == v1alpha1.PhaseSucceeded {
			return ctrl.Result{}, r.appendArtifactEvent(ctx, &artifact, phase, condition.Reason)
		}
		return ctrl.Result{}, nil
	}

	// Update artifact status
	artifact.Status.Phase = phase
	artifact.Status.ObservedGeneration = artifact.Generation
	apiMeta.SetStatusCondition(&artifact.Status.Conditions, condition)
	if err := r.Status().Update(ctx, &artifact); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.appendArtifactEvent(ctx, &artifact, phase, condition.Reason)
}

func (r *ArtifactReconciler) validateStoredArtifact(ctx context.Context, artifact *v1alpha1.Artifact) (string, string) {
	if artifact.Spec.Contract.Name == "" || artifact.Spec.Contract.Version == "" || artifact.Spec.Path == "" {
		return "InvalidMetadata", "artifact requires a versioned contract and content path"
	}
	if err := artifacts.ValidateClaims(artifact.Spec.Contract, artifact.Spec.Claims); err != nil {
		return "InvalidClaims", err.Error()
	}
	if artifact.Spec.ProducerRef.Kind == "SovereignWorkflow" {
		return r.validateBootstrapArtifact(ctx, artifact)
	}
	return "", ""
}

func (r *ArtifactReconciler) validateBootstrapArtifact(ctx context.Context, artifact *v1alpha1.Artifact) (string, string) {
	var workflow v1alpha1.SovereignWorkflow
	key := types.NamespacedName{Namespace: artifact.Namespace, Name: artifact.Spec.ProducerRef.Name}
	if err := r.Get(ctx, key, &workflow); err != nil {
		return "BootstrapWorkflowUnavailable", "bootstrap workflow is unavailable"
	}
	if err := ValidateBootstrapArtifactIdentity(&workflow, artifact); err != nil {
		return "InvalidBootstrapProvenance", err.Error()
	}
	_, changeRequest, err := LoadBootstrapChangeRequest(ctx, r.Client, &workflow)
	if err != nil {
		return "InvalidBootstrapSource", err.Error()
	}
	expectedClaims, err := artifacts.ProjectChangeRequestClaims(changeRequest)
	if err != nil || !reflect.DeepEqual(artifact.Spec.Claims, expectedClaims) {
		return "InvalidBootstrapClaims", "bootstrap Artifact claims do not match the exact admitted change request"
	}
	if artifact.Spec.SourceRevision != changeRequest.SourceCommit {
		return "InvalidBootstrapProvenance", "bootstrap Artifact source revision does not match the change request"
	}
	jobName := workflow.Status.BootstrapJobRef
	if jobName == "" {
		jobName = BootstrapJobName(workflow.Name)
	}
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: workflow.Namespace, Name: jobName}, &job); err != nil {
		return "BootstrapVerificationUnavailable", "bootstrap storage Job is unavailable"
	}
	if !metav1.IsControlledBy(&job, &workflow) || job.Status.Succeeded == 0 || job.Status.Failed > 0 {
		return "BootstrapVerificationFailed", "bootstrap storage Job did not complete successfully"
	}
	return "", ""
}

func (r *ArtifactReconciler) appendArtifactEvent(ctx context.Context, artifact *v1alpha1.Artifact, phase v1alpha1.ResourcePhase, reason string) error {
	eventType := "ArtifactAccepted"
	outcome := "accepted"
	if phase == v1alpha1.PhaseFailed {
		eventType = "ArtifactRejected"
		outcome = "rejected"
	}
	var data any = map[string]any{
		"contract":       artifact.Spec.Contract,
		"classification": artifact.Spec.Classification,
		"sourceRevision": artifact.Spec.SourceRevision,
	}
	if phase == v1alpha1.PhaseSucceeded {
		decisionEvent, err := r.producerDecisionEvent(ctx, artifact)
		if err != nil {
			return err
		}
		evidence := ArtifactEvidence(artifact)
		data = audit.ConsequenceRecorded{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			DecisionEvent: decisionEvent,
			Consequences: []audit.ConsequenceEvidence{{
				SchemaVersion: audit.PayloadSchemaVersionV1,
				Kind:          "artifact-accepted",
				Resource:      &evidence.Artifact,
				Artifact:      &evidence,
				Digest:        artifact.Spec.Digest,
				Target:        artifact.Spec.Contract.Name + "/" + artifact.Spec.Contract.Version,
			}},
		}
	}
	return audit.AppendControllerEvent(ctx, r.Audit, "artifact-controller", nil, audit.EventOptions{
		Type: eventType,
		Subject: audit.Subject{
			Namespace: artifact.Namespace,
			Workflow:  artifact.Spec.WorkflowRef.Name,
		},
		Action:  "validate",
		Target:  artifact.Name,
		Outcome: outcome,
		Reason:  reason,
		References: map[string]string{
			"artifact": artifact.Name,
			"digest":   artifact.Spec.Digest,
			"path":     artifact.Spec.Path,
			"producer": artifact.Spec.ProducerRef.Kind + "/" + artifact.Spec.ProducerRef.Name,
		},
		Data: data,
	})
}

func (r *ArtifactReconciler) producerDecisionEvent(ctx context.Context, artifact *v1alpha1.Artifact) (string, error) {
	if r.Audit == nil || artifact.Spec.ProducerRef.Kind == "SovereignWorkflow" {
		return "", nil
	}
	events, err := r.Audit.ListWorkflow(ctx, artifact.Spec.WorkflowRef.Name)
	if err != nil {
		return "", err
	}
	wantedType := map[string]string{
		"AgentRun":         "AgentExecutionAuthorized",
		"UtilityOperation": "UtilityExecutionAuthorized",
		"ValidationRun":    "ValidationEvaluated",
	}[artifact.Spec.ProducerRef.Kind]
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != wantedType {
			continue
		}
		if event.Target == artifact.Spec.ProducerRef.Name ||
			event.References["agentRun"] == artifact.Spec.ProducerRef.Name ||
			event.References["utilityOperation"] == artifact.Spec.ProducerRef.Name ||
			event.References["validationRun"] == artifact.Spec.ProducerRef.Name {
			return event.ID, nil
		}
	}
	return "", nil
}
