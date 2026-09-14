package artifacts

import (
	"context"
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ArtifactAccepted(artifact *v1alpha1.Artifact) bool {
	if artifact.Status.Phase != v1alpha1.PhaseSucceeded ||
		artifact.Status.ObservedGeneration != artifact.Generation {
		return false
	}
	condition := apiMeta.FindStatusCondition(artifact.Status.Conditions, "Valid")
	return condition != nil &&
		condition.Status == metav1.ConditionTrue &&
		condition.ObservedGeneration == artifact.Generation
}

// ResolvePinnedInput verifies the exact artifact identity selected by the
// workflow controller. A missing or not-yet-accepted artifact is reported as
// not ready; an identity mismatch or rejected artifact is invalid.
func ResolvePinnedInput(
	ctx context.Context,
	reader client.Reader,
	namespace string,
	workflowRef v1alpha1.UIDReference,
	requested v1alpha1.ArtifactReference,
) (*v1alpha1.Artifact, bool, string, error) {
	if requested.ArtifactRef == nil {
		return nil, false, "", nil
	}

	var artifact v1alpha1.Artifact
	key := types.NamespacedName{
		Namespace: namespace,
		Name:      requested.ArtifactRef.Name,
	}
	if err := reader.Get(ctx, key, &artifact); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, "", nil
		}
		return nil, false, "", fmt.Errorf(
			"get pinned input artifact %s: %w",
			key.String(),
			err,
		)
	}

	if artifact.UID != requested.ArtifactRef.UID {
		return nil, false, fmt.Sprintf(
			"input artifact %q UID does not match its pinned identity",
			requested.Name,
		), nil
	}
	if artifact.Spec.WorkflowRef != workflowRef {
		return nil, false, fmt.Sprintf(
			"input artifact %q belongs to a different workflow",
			requested.Name,
		), nil
	}
	if artifact.Spec.Contract.Name != requested.Name {
		return nil, false, fmt.Sprintf(
			"input artifact %q resolves to contract %q",
			requested.Name,
			artifact.Spec.Contract.Name,
		), nil
	}
	if requested.Digest == "" || artifact.Spec.Digest != requested.Digest {
		return nil, false, fmt.Sprintf(
			"input artifact %q digest does not match its pinned identity",
			requested.Name,
		), nil
	}
	if requested.ProducerAttemptRef != "" &&
		artifact.Spec.ProducerRef.Name != requested.ProducerAttemptRef {
		return nil, false, fmt.Sprintf(
			"input artifact %q producer does not match attempt %q",
			requested.Name,
			requested.ProducerAttemptRef,
		), nil
	}

	if ArtifactAccepted(&artifact) {
		return &artifact, true, "", nil
	}
	if artifact.Status.Phase == v1alpha1.PhaseFailed {
		return nil, false, fmt.Sprintf(
			"input artifact %q was rejected",
			requested.Name,
		), nil
	}
	return nil, false, "", nil
}
