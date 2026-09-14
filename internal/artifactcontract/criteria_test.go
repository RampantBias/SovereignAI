package artifactcontract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const authoringRequest = `{"summary":"Add division",` +
	`"description":"Calculator division",` +
	`"acceptanceCriteria":["84 / 2 returns 42","Division by zero returns an error"],` +
	`"repositoryURL":"https://git.example.test/calculator.git",` +
	`"sourceCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`

func preparedRequest(t *testing.T) ([]byte, ChangeRequest) {
	t.Helper()
	data, err := PrepareChangeRequest([]byte(authoringRequest))
	if err != nil {
		t.Fatal(err)
	}
	var cr ChangeRequest
	if err := json.Unmarshal(data, &cr); err != nil {
		t.Fatal(err)
	}
	return data, cr
}

func TestCriteriaAuthoringAndStableIdentity(t *testing.T) {
	data, cr := preparedRequest(t)
	if cr.AcceptanceCriteria[0].ID != "RQ-001" || cr.AcceptanceCriteria[1].ID != "RQ-002" {
		t.Fatal("text requirements need deterministic IDs")
	}
	if cr.AcceptanceCriteria[0].Text != "84 / 2 returns 42" {
		t.Fatal("text changed")
	}
	if err := ValidateContract(ChangeRequestContract, data); err != nil {
		t.Fatal(err)
	}
	if err := ValidateContract(ChangeRequestContract, []byte(authoringRequest)); err == nil {
		t.Fatal("stored contract must reject text-only authoring")
	}
	// Fully structured bytes are not silently rewritten.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		t.Fatal(err)
	}
	same, err := PrepareChangeRequest(pretty.Bytes())
	if err != nil || !bytes.Equal(same, pretty.Bytes()) {
		t.Fatalf("structured bytes changed: %v", err)
	}
	again, err := PrepareChangeRequest([]byte(authoringRequest))
	if err != nil || !bytes.Equal(again, data) {
		t.Fatal("authoring is not deterministic")
	}
	// Structured revisions keep IDs, regardless of order, and recompute only omitted digests.
	original := cr.AcceptanceCriteria[0]
	slices.Reverse(cr.AcceptanceCriteria)
	cr.AcceptanceCriteriaSetDigest = ""
	cr.AcceptanceCriteria[1].Text = "84 / 2 returns exactly 42"
	cr.AcceptanceCriteria[1].Digest = ""
	draft, _ := json.Marshal(cr)
	revised, err := PrepareChangeRequest(draft)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(revised, &cr); err != nil {
		t.Fatal(err)
	}
	if cr.AcceptanceCriteria[1].ID != original.ID || cr.AcceptanceCriteria[1].Digest == original.Digest {
		t.Fatal("revision lost identity or retained stale digest")
	}
}

func TestCriteriaCanonicalRepresentation(t *testing.T) {
	// These literal vectors are independently computed from the documented JSON encoding.
	if got := CriterionDigest("RQ-001", "84 / 2 returns 42"); got != "sha256:6ed7267c4acbd814ac73e6ff6fe256964162eee90ef24f2050da07a3d15687b9" {
		t.Fatalf("canonical encoding changed: %s", got)
	}
	if got := CriteriaSetDigest([]CriterionIdentityV1{{
		ID:     "RQ-001",
		Digest: "sha256:6ed7267c4acbd814ac73e6ff6fe256964162eee90ef24f2050da07a3d15687b9"}}); got != "sha256:9a78ef314f7aab43e9c0f3d5d410f4d6579473d01184189573b0699f5ab2383b" {
		t.Fatalf("set encoding changed: %s", got)
	}
	data, cr := preparedRequest(t)
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	reorderedKeys, _ := json.Marshal(parsed)
	var other ChangeRequest
	if err := json.Unmarshal(reorderedKeys, &other); err != nil {
		t.Fatal(err)
	}
	if err := other.Validate(); err != nil {
		t.Fatal(err)
	}
	if other.AcceptanceCriteriaSetDigest != cr.AcceptanceCriteriaSetDigest {
		t.Fatal("JSON object order affected criteria")
	}
	ids := CriterionIdentities(cr.AcceptanceCriteria)
	before := CriteriaSetDigest(ids)
	slices.Reverse(ids)
	if before == CriteriaSetDigest(ids) {
		t.Fatal("ordered set digest ignored order")
	}
	for _, text := range []string{"84 / 2 returns 42 ", "84 / 2 returns 42\n", "84 / 2 returns 42\r\n"} {
		if CriterionDigest("RQ-001", text) == cr.AcceptanceCriteria[0].Digest {
			t.Fatal("text was silently normalized")
		}
	}
}

func TestCriteriaTamperingFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ChangeRequest)
	}{
		{"missing", func(c *ChangeRequest) { c.AcceptanceCriteria = nil }},
		{"duplicate-id", func(c *ChangeRequest) { c.AcceptanceCriteria[1].ID = c.AcceptanceCriteria[0].ID }},
		{"empty-id", func(c *ChangeRequest) { c.AcceptanceCriteria[0].ID = "" }},
		{"invalid-id", func(c *ChangeRequest) { c.AcceptanceCriteria[0].ID = "bad id" }},
		{"empty-text", func(c *ChangeRequest) { c.AcceptanceCriteria[0].Text = "" }},
		{"blank-text", func(c *ChangeRequest) { c.AcceptanceCriteria[0].Text = " \t\n" }},
		{"duplicate-text", func(c *ChangeRequest) {
			c.AcceptanceCriteria[1].Text = c.AcceptanceCriteria[0].Text
			c.AcceptanceCriteria[1].Digest = CriterionDigest(c.AcceptanceCriteria[1].ID, c.AcceptanceCriteria[1].Text)
			c.AcceptanceCriteriaSetDigest = CriteriaSetDigest(CriterionIdentities(c.AcceptanceCriteria))
		}},
		{"oversized-text", func(c *ChangeRequest) { c.AcceptanceCriteria[0].Text = strings.Repeat("x", 4097) }},
		{"too-many", func(c *ChangeRequest) {
			for len(c.AcceptanceCriteria) < 33 {
				c.AcceptanceCriteria = append(c.AcceptanceCriteria, c.AcceptanceCriteria[0])
			}
		}},
		{"altered-text", func(c *ChangeRequest) { c.AcceptanceCriteria[0].Text = "84 / 2 returns 41" }},
		{"missing-digest", func(c *ChangeRequest) { c.AcceptanceCriteria[0].Digest = "" }},
		{"malformed-digest", func(c *ChangeRequest) { c.AcceptanceCriteria[0].Digest = "SHA256:invalid" }},
		{"missing-set", func(c *ChangeRequest) { c.AcceptanceCriteriaSetDigest = "" }},
		{"wrong-set", func(c *ChangeRequest) { c.AcceptanceCriteriaSetDigest = "sha256:" + strings.Repeat("0", 64) }},
		{"reordered-stale-set", func(c *ChangeRequest) { slices.Reverse(c.AcceptanceCriteria) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cr := preparedRequest(t)
			tc.mutate(&cr)
			data, _ := json.Marshal(cr)
			if err := ValidateContract(ChangeRequestContract, data); err == nil {
				t.Fatal("invalid stored criteria accepted")
			}
		})
	}
	for _, name := range []string{"altered-text", "wrong-set", "reordered-stale-set", "duplicate-id"} {
		t.Run("authoring-does-not-repair-"+name, func(t *testing.T) {
			_, cr := preparedRequest(t)
			for _, tc := range cases {
				if tc.name == name {
					tc.mutate(&cr)
				}
			}
			data, _ := json.Marshal(cr)
			if _, err := PrepareChangeRequest(data); err == nil {
				t.Fatal("supplied bad digest or ID repaired silently")
			}
		})
	}
}

func TestCalculatorAuthoringExamplesPrepare(t *testing.T) {
	for _, path := range []string{"demo/change-requests/calculator-divide.v1.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := PrepareChangeRequest(data)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if err := ValidateContract(ChangeRequestContract, canonical); err != nil {
			t.Fatal(err)
		}
	}
}
