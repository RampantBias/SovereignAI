package artifacts

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
)

var (
	claimDigestPattern        = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	claimGitOIDPattern        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	claimOCIRepositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+$`)
	claimBranchPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

// Derives the validationrun projection from the exact bytes that were stored and validated by the collector
func projectClaims(contract v1alpha1.ContractReference, content []byte) (*v1alpha1.ArtifactClaims, error) {
	switch contract.Name + "/" + contract.Version {
	case artifactcontract.ChangeRequestContract:
		if err := artifactcontract.ValidateContract(artifactcontract.ChangeRequestContract, content); err != nil {
			return nil, err
		}
		var value artifactcontract.ChangeRequest
		if err := json.Unmarshal(content, &value); err != nil {
			return nil, err
		}
		return ProjectChangeRequestClaims(value)
	case artifactcontract.CandidateRevisionContract:
		var value artifactcontract.CandidateRevision
		if err := json.Unmarshal(content, &value); err != nil {
			return nil, fmt.Errorf("decode candidate revision claims: %w", err)
		}
		return &v1alpha1.ArtifactClaims{CandidateRevision: &v1alpha1.CandidateRevisionClaims{
			RepositoryURL: value.RepositoryURL,
			Branch:        value.Branch,
			Commit:        value.Commit,
			Tree:          value.Tree,
		}}, nil
	case artifactcontract.CandidateRemoteProofContract:
		var value artifactcontract.CandidateRemoteProof
		if err := json.Unmarshal(content, &value); err != nil {
			return nil, fmt.Errorf("decode candidate remote proof claims: %w", err)
		}
		return &v1alpha1.ArtifactClaims{CandidateRemoteProof: &v1alpha1.CandidateRemoteProofClaims{
			CandidateRevisionDigest: value.CandidateRevisionDigest,
			RepositoryURL:           value.RepositoryURL,
			Ref:                     value.Ref,
			ObservedCommit:          value.ObservedCommit,
			VerifiedAt:              value.VerifiedAt,
		}}, nil
	case artifactcontract.ImageDigestContract:
		var value artifactcontract.ImageDigest
		if err := json.Unmarshal(content, &value); err != nil {
			return nil, fmt.Errorf("decode image digest claims: %w", err)
		}
		return &v1alpha1.ArtifactClaims{ImageDigest: &v1alpha1.ImageDigestClaims{
			CandidateRevisionDigest: value.CandidateRevisionDigest,
			ImageRepository:         value.ImageRepository,
			OCIDigest:               value.Digest,
			CandidateCommit:         value.CandidateCommit,
			CandidateTree:           value.CandidateTree,
		}}, nil
	default:
		return nil, nil
	}
}

// Validates the contract-to-claims binding and the shape ofevery projected identity
func ValidateClaims(contract v1alpha1.ContractReference, claims *v1alpha1.ArtifactClaims) error {
	key := contract.Name + "/" + contract.Version
	if claims == nil {
		switch key {
		case artifactcontract.ChangeRequestContract, artifactcontract.CandidateRevisionContract,
			artifactcontract.CandidateRemoteProofContract,
			artifactcontract.ImageDigestContract:
			return fmt.Errorf("contract %q requires controller-readable claims", key)
		default:
			return nil
		}
	}

	// must be one type
	claimCount := 0
	if claims.ChangeRequest != nil {
		claimCount++
	}
	if claims.CandidateRevision != nil {
		claimCount++
	}
	if claims.CandidateRemoteProof != nil {
		claimCount++
	}
	if claims.ImageDigest != nil {
		claimCount++
	}
	if claimCount != 1 {
		return fmt.Errorf("artifact claims must contain exactly one contract projection")
	}

	switch key {
	case artifactcontract.ChangeRequestContract:
		if claims.ChangeRequest == nil {
			return fmt.Errorf("contract %q requires changeRequest claims", key)
		}
		identities := make([]artifactcontract.CriterionIdentityV1, len(claims.ChangeRequest.AcceptanceCriteria))
		for i, criterion := range claims.ChangeRequest.AcceptanceCriteria {
			identities[i] = artifactcontract.CriterionIdentityV1{ID: criterion.ID, Digest: criterion.Digest}
		}
		return artifactcontract.ValidateCriterionIdentities(identities, claims.ChangeRequest.AcceptanceCriteriaSetDigest)
	case artifactcontract.CandidateRevisionContract:
		value := claims.CandidateRevision
		if value == nil {
			return fmt.Errorf("contract %q requires candidateRevision claims", key)
		}
		if err := validateClaimsRepository(value.RepositoryURL); err != nil {
			return fmt.Errorf("candidateRevision.repositoryURL: %w", err)
		}
		if !claimBranchPattern.MatchString(value.Branch) {
			return fmt.Errorf("candidateRevision.branch is invalid")
		}
		if !claimGitOIDPattern.MatchString(value.Commit) || !claimGitOIDPattern.MatchString(value.Tree) {
			return fmt.Errorf("candidateRevision commit and tree must be full lowercase Git object IDs")
		}
	case artifactcontract.CandidateRemoteProofContract:
		value := claims.CandidateRemoteProof
		if value == nil {
			return fmt.Errorf("contract %q requires candidateRemoteProof claims", key)
		}
		if !claimDigestPattern.MatchString(value.CandidateRevisionDigest) {
			return fmt.Errorf("candidateRemoteProof.candidateRevisionDigest is invalid")
		}
		if err := validateClaimsRepository(value.RepositoryURL); err != nil {
			return fmt.Errorf("candidateRemoteProof.repositoryURL: %w", err)
		}
		branch, found := strings.CutPrefix(value.Ref, "refs/heads/")
		if !found || !claimBranchPattern.MatchString(branch) {
			return fmt.Errorf("candidateRemoteProof.ref must identify a valid branch")
		}
		if !claimGitOIDPattern.MatchString(value.ObservedCommit) {
			return fmt.Errorf("candidateRemoteProof.observedCommit is invalid")
		}
		if _, err := time.Parse(time.RFC3339, value.VerifiedAt); err != nil {
			return fmt.Errorf("candidateRemoteProof.verifiedAt is invalid")
		}
	case artifactcontract.ImageDigestContract:
		value := claims.ImageDigest
		if value == nil {
			return fmt.Errorf("contract %q requires imageDigest claims", key)
		}
		if !claimDigestPattern.MatchString(value.CandidateRevisionDigest) || !claimDigestPattern.MatchString(value.OCIDigest) {
			return fmt.Errorf("imageDigest lineage and OCI digests are invalid")
		}
		if !claimOCIRepositoryPattern.MatchString(value.ImageRepository) {
			return fmt.Errorf("imageDigest.imageRepository is invalid")
		}
		if !claimGitOIDPattern.MatchString(value.CandidateCommit) || !claimGitOIDPattern.MatchString(value.CandidateTree) {
			return fmt.Errorf("imageDigest candidate commit and tree must be full lowercase Git object IDs")
		}
	default:
		return fmt.Errorf("contract %q does not define controller-readable claims", key)
	}
	return nil
}

func validateClaimsRepository(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be an absolute credential-free HTTPS repository URL")
	}
	return nil
}
