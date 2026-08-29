package artifacts

import (
	"github.com/SovereignAI/internal/api/v1alpha1"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
