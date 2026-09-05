package artifacts

import (
	"encoding/json"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"strings"
	"testing"
)

func TestChangeRequestClaimsRequireExactValidatedCriteria(t *testing.T) {
	data, err := artifactcontract.PrepareChangeRequest([]byte(`{"summary":"division","description":"division","acceptanceCriteria":["84 / 2 returns 42"],"repositoryURL":"https://example.test/repo.git","sourceCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	if err != nil {
		t.Fatal(err)
	}
	contract := v1alpha1.ContractReference{Name: "change-request", Version: "v1"}
	claims, err := projectClaims(contract, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateClaims(contract, claims); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*v1alpha1.ArtifactClaims){
		func(c *v1alpha1.ArtifactClaims) { c.ChangeRequest.AcceptanceCriteria = nil },
		func(c *v1alpha1.ArtifactClaims) { c.ChangeRequest.AcceptanceCriteria[0].ID = "AC-999" },
		func(c *v1alpha1.ArtifactClaims) {
			c.ChangeRequest.AcceptanceCriteria[0].Digest = "sha256:" + strings.Repeat("0", 64)
		},
		func(c *v1alpha1.ArtifactClaims) { c.ChangeRequest.AcceptanceCriteriaSetDigest = "" },
		func(c *v1alpha1.ArtifactClaims) { c.CandidateRevision = &v1alpha1.CandidateRevisionClaims{} },
	} {
		altered := claims.DeepCopy()
		mutate(altered)
		if err := ValidateClaims(contract, altered); err == nil {
			t.Fatal("invalid claims accepted")
		}
		if err := ValidateClaims(contract, claims); err != nil {
			t.Fatal("deep copy mutation changed original")
		}
	}
	if err := ValidateClaims(contract, nil); err == nil {
		t.Fatal("missing criteria claims accepted")
	}
	if err := ValidateClaims(v1alpha1.ContractReference{Name: "image-digest", Version: "v1"}, claims); err == nil {
		t.Fatal("claims moved to wrong contract")
	}
	altered := strings.Replace(string(data), "84 / 2 returns 42", "84 / 2 returns 41", 1)
	if _, err := projectClaims(contract, []byte(altered)); err == nil {
		t.Fatal("claims projected from altered bytes")
	}
	var cr artifactcontract.ChangeRequest
	if err := json.Unmarshal(data, &cr); err != nil {
		t.Fatal(err)
	}
	cr.AcceptanceCriteria[0].Text = "changed"
	if _, err := ProjectChangeRequestClaims(cr); err == nil {
		t.Fatal("unvalidated request projected")
	}
}
