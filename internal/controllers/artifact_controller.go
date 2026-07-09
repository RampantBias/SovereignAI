package controllers

import (
	"context"
	"strings"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// / Artifact Controller is responsible for validating artifact metadata/digest
type ArtifactReconciler struct {
	client.Client
	Audit audit.Recorder
}

func (r *ArtifactReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.Artifact{}).Complete(r)
}

func (r *ArtifactReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var artifact v1alpha1.Artifact
	if err := r.Get(ctx, request.NamespacedName, &artifact); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	phase := v1alpha1.PhaseSucceeded
	condition := metav1.Condition{Type: "Valid", Status: metav1.ConditionTrue, Reason: "ContractAccepted", Message: "artifact metadata is valid", ObservedGeneration: artifact.Generation}

	// Verify validity of artifact metadata with a valid digest, contract name & version, and location
	if !strings.HasPrefix(artifact.Spec.Digest, "sha256:") || artifact.Spec.Contract.Name == "" || artifact.Spec.Contract.Version == "" || artifact.Spec.Path == "" {
		phase = v1alpha1.PhaseFailed
		condition.Status = metav1.ConditionFalse
		condition.Reason = "InvalidMetadata"
		condition.Message = "artifact requires a sha256 digest, versioned contract, and content path"
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
			"producer": artifact.Spec.ProducerRef,
		},
		Data: map[string]any{
			"contract":       artifact.Spec.Contract,
			"classification": artifact.Spec.Classification,
			"sourceRevision": artifact.Spec.SourceRevision,
		},
	})
}
