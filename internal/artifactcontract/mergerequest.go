package artifactcontract

import (
	"fmt"
	"strconv"
	"strings"
)

// MergeRequest records review creation, not proof that a target was merged.
type MergeRequest struct {
	CandidateRevisionDigest    string         `json:"candidateRevisionDigest"`
	CandidateRemoteProofDigest string         `json:"candidateRemoteProofDigest"`
	ValidationResultDigest     string         `json:"validationResultDigest"`
	RepositoryURL              string         `json:"repositoryURL"`
	Provider                   string         `json:"provider"`
	Number                     int            `json:"number"`
	URL                        string         `json:"url"`
	SourceBranch               string         `json:"sourceBranch"`
	TargetBranch               string         `json:"targetBranch"`
	CandidateCommit            string         `json:"candidateCommit"`
	State                      string         `json:"state"`
	UtilityOperation           ObjectIdentity `json:"utilityOperation"`
	IdempotencyKey             string         `json:"idempotencyKey"`
}

func (v MergeRequest) Validate() error {
	for field, value := range map[string]string{
		"candidateRevisionDigest":    v.CandidateRevisionDigest,
		"candidateRemoteProofDigest": v.CandidateRemoteProofDigest,
		"validationResultDigest":     v.ValidationResultDigest,
	} {
		if err := validateDigestField(field, value); err != nil {
			return err
		}
	}
	if err := validateRepositoryURL(v.RepositoryURL); err != nil {
		return fieldError("repositoryURL", err)
	}
	for field, value := range map[string]string{"sourceBranch": v.SourceBranch, "targetBranch": v.TargetBranch} {
		if err := validateBranch(value); err != nil {
			return fieldError(field, err)
		}
	}
	if v.SourceBranch == v.TargetBranch {
		return fmt.Errorf("source and target branches must differ")
	}
	if err := validateGitOIDField("candidateCommit", v.CandidateCommit); err != nil {
		return err
	}
	if v.Provider != "github" || v.Number < 1 {
		return fmt.Errorf("merge request requires provider github and a positive number")
	}
	if err := validateRepositoryURL(v.URL); err != nil {
		return fieldError("url", err)
	}
	if !strings.HasPrefix(v.RepositoryURL, "https://github.com/") ||
		!strings.EqualFold(v.URL, strings.TrimSuffix(v.RepositoryURL, ".git")+"/pull/"+strconv.Itoa(v.Number)) {
		return fmt.Errorf("merge request URL must identify its repository and number")
	}
	if v.State != "open" && v.State != "merged" {
		return fmt.Errorf("merge request state must be open or merged")
	}
	if err := validateObjectIdentity("utilityOperation", v.UtilityOperation); err != nil {
		return err
	}
	if strings.TrimSpace(v.IdempotencyKey) == "" || len(v.IdempotencyKey) > 1024 {
		return fmt.Errorf("idempotencyKey must contain 1-1024 bytes")
	}
	return nil
}
