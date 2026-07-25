package controllers

import (
	"context"
	"fmt"
	"os"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	terminating, err := namespaceTerminating(ctx, r.Client, artifact.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if terminating {
		return ctrl.Result{}, nil
	}

	phase := v1alpha1.PhaseSucceeded
	condition := metav1.Condition{
		Type:               "Valid",
		Status:             metav1.ConditionTrue,
		Reason:             "ContractAccepted",
		Message:            "stored bytes, digest, and typed contract are valid",
		ObservedGeneration: artifact.Generation,
	}
	reason, message := r.validateStoredArtifact(&artifact)
	if reason != "" {
		phase = v1alpha1.PhaseFailed
		condition.Status = metav1.ConditionFalse
		condition.Reason = reason
		condition.Message = message
	}

	if artifact.Status.Phase == phase && artifact.Status.ObservedGeneration == artifact.Generation {
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

func (r *ArtifactReconciler) validateStoredArtifact(artifact *v1alpha1.Artifact) (string, string) {
	if artifact.Spec.Contract.Name == "" || artifact.Spec.Contract.Version == "" || artifact.Spec.Path == "" {
		return "InvalidMetadata", "artifact requires a versioned contract and content path"
	}
	info, err := os.Lstat(artifact.Spec.Path)
	if err != nil {
		return "ContentUnavailable", "stored artifact content is unavailable"
	}
	if !info.Mode().IsRegular() {
		return "InvalidContent", "stored artifact content must be a regular file"
	}
	if info.Size() > artifactcontract.MaxArtifactBytes {
		return "ContentTooLarge", fmt.Sprintf("stored artifact exceeds %d-byte limit", artifactcontract.MaxArtifactBytes)
	}
	content, err := os.ReadFile(artifact.Spec.Path)
	if err != nil {
		return "ContentUnavailable", "stored artifact content cannot be read"
	}
	actualDigest := artifactcontract.DigestBytes(content)
	if artifact.Spec.Digest != actualDigest {
		return "DigestMismatch", fmt.Sprintf("stored artifact digest does not match declared digest: got %s", actualDigest)
	}
	registry := r.Contracts
	if registry == nil {
		registry = artifactcontract.DefaultRegistry()
	}
	if err := registry.Validate(artifact.Spec.Contract.Name, artifact.Spec.Contract.Version, content); err != nil {
		return "ContractRejected", err.Error()
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
	return appendControllerEvent(ctx, r.Audit, "artifact-controller", nil, audit.EventOptions{
		Type: eventType,
		Subject: audit.Subject{
			Namespace: artifact.Namespace,
			Workflow:  artifact.Spec.WorkflowRef,
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
		Data: map[string]any{
			"contract":       artifact.Spec.Contract,
			"classification": artifact.Spec.Classification,
			"sourceRevision": artifact.Spec.SourceRevision,
		},
	})
}
