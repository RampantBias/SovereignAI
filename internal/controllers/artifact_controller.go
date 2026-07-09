package controllers

import (
	"context"
	"strings"

	"github.com/SovereignAI/internal/api/v1alpha1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// / Artifact Controller is responsible for validating artifact metadata/digest
type ArtifactReconciler struct{ client.Client }

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

	// TODO: I'm not sure what this phase check is doing.
	if artifact.Status.Phase == phase && artifact.Status.ObservedGeneration == artifact.Generation {
		return ctrl.Result{}, nil
	}

	// Update artifact status
	artifact.Status.Phase = phase
	artifact.Status.ObservedGeneration = artifact.Generation
	apiMeta.SetStatusCondition(&artifact.Status.Conditions, condition)
	return ctrl.Result{}, r.Status().Update(ctx, &artifact)
}
