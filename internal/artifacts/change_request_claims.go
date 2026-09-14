package artifacts

import (
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
)

// ProjectChangeRequestClaims only projects a validated canonical request.
// Callers must obtain it from the exact stored or immutable bootstrap bytes.
func ProjectChangeRequestClaims(value artifactcontract.ChangeRequest) (*v1alpha1.ArtifactClaims, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	criteria := make([]v1alpha1.CriterionIdentity, len(value.AcceptanceCriteria))
	for i, criterion := range value.AcceptanceCriteria {
		criteria[i] = v1alpha1.CriterionIdentity{ID: criterion.ID, Digest: criterion.Digest}
	}
	return &v1alpha1.ArtifactClaims{ChangeRequest: &v1alpha1.ChangeRequestClaims{
		AcceptanceCriteria: criteria, AcceptanceCriteriaSetDigest: value.AcceptanceCriteriaSetDigest,
	}}, nil
}
