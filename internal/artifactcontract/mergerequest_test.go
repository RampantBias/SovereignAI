package artifactcontract

import "testing"

func TestMergeRequestEvidenceRejectsInvalidIdentity(t *testing.T) {
	fixture := fixtureForContract(t, MergeRequestContract)
	for _, test := range []struct {
		name, field string
		value       any
	}{
		{"wrong repository URL", "url", "https://github.com/attacker/calculator/pull/42"},
		{"wrong request number", "number", float64(43)},
		{"same source and target", "targetBranch", fixture.Document["sourceBranch"]},
		{"closed without merging", "state", "closed"},
		{"missing candidate evidence", "candidateRevisionDigest", ""},
		{"missing push evidence", "candidateRemoteProofDigest", ""},
		{"missing validation evidence", "validationResultDigest", ""},
		{"missing idempotency identity", "idempotencyKey", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := cloneDocument(t, fixture.Document)
			document[test.field] = test.value
			if err := ValidateContract(MergeRequestContract, fixtureBytes(t, document)); err == nil {
				t.Fatal("invalid merge request evidence was accepted")
			}
		})
	}
}
