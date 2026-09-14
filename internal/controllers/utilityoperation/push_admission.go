package utilityoperation

import (
	"context"
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
)

// Candidate pushes take their branch from the accepted input, just as GitPush.Run
// does. Caller parameters must not substitute a different policy target.
func (r *UtilityOperationReconciler) candidatePushBranch(ctx context.Context, operation *v1alpha1.UtilityOperation, project *v1alpha1.SovereignProject) (string, error) {
	var pin *v1alpha1.ArtifactReference
	for i := range operation.Spec.Inputs {
		input := &operation.Spec.Inputs[i]
		if input.Name != "candidate-revision" {
			continue
		}
		if pin != nil {
			return "", fmt.Errorf("candidate push requires exactly one candidate-revision input")
		}
		pin = input
	}
	if pin == nil || pin.ArtifactRef == nil || pin.ArtifactRef.Name == "" || pin.ArtifactRef.UID == "" || pin.Digest == "" {
		return "", fmt.Errorf("candidate push requires an immutable candidate-revision pin")
	}
	artifact, ready, reason, err := artifacts.ResolvePinnedInput(ctx, r.Client, operation.Namespace, operation.Spec.WorkflowRef, *pin)
	if err != nil {
		return "", err
	}
	if reason != "" {
		return "", fmt.Errorf("candidate push: %s", reason)
	}
	if !ready {
		return "", fmt.Errorf("candidate push input is not accepted yet")
	}
	if artifact.Spec.Contract.Name+"/"+artifact.Spec.Contract.Version != artifactcontract.CandidateRevisionContract {
		return "", fmt.Errorf("candidate push requires %s", artifactcontract.CandidateRevisionContract)
	}
	if err := artifacts.ValidateClaims(artifact.Spec.Contract, artifact.Spec.Claims); err != nil {
		return "", fmt.Errorf("candidate push claims: %w", err)
	}
	candidate := artifact.Spec.Claims.CandidateRevision
	if candidate.RepositoryURL != project.Spec.ApplicationRepository.URL {
		return "", fmt.Errorf("candidate push repository does not match the project repository")
	}
	return candidate.Branch, nil
}
