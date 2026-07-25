package artifactcontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type contractFixture struct {
	Contract string         `json:"contract"`
	Document map[string]any `json:"document"`
}

func loadFixtures(t *testing.T) []contractFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []contractFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func fixtureBytes(t *testing.T, document map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRegistryContainsAndAcceptsEveryFrozenArtifactContract(t *testing.T) {
	fixtures := loadFixtures(t)
	registry := DefaultRegistry()
	if len(fixtures) != 10 {
		t.Fatalf("fixture count = %d, want 10", len(fixtures))
	}
	if len(registry.Contracts()) != 10 {
		t.Fatalf("registered contract count = %d, want 10: %v", len(registry.Contracts()), registry.Contracts())
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Contract, func(t *testing.T) {
			if err := ValidateContract(fixture.Contract, fixtureBytes(t, fixture.Document)); err != nil {
				t.Fatalf("valid fixture rejected: %v", err)
			}
		})
	}
}

func TestCheckedInCalculatorChangeRequestMatchesFrozenContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "demo", "change-requests", "calculator-divide.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateContract(ChangeRequestContract, data); err != nil {
		t.Fatalf("checked-in calculator change request is invalid: %v", err)
	}
}

func TestRegistryRejectsUnknownFieldsForEveryContract(t *testing.T) {
	for _, fixture := range loadFixtures(t) {
		t.Run(fixture.Contract, func(t *testing.T) {
			fixture.Document["unexpected"] = true
			err := ValidateContract(fixture.Contract, fixtureBytes(t, fixture.Document))
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("expected unknown-field rejection, got %v", err)
			}
		})
	}
}

func TestRegistryRejectsUnregisteredAndMalformedContracts(t *testing.T) {
	for _, contract := range []string{"unknown/v1", "change-request", "change-request/v1/extra", "/v1", "change-request/"} {
		t.Run(contract, func(t *testing.T) {
			if err := ValidateContract(contract, []byte(`{}`)); err == nil {
				t.Fatal("expected contract rejection")
			}
		})
	}
}

func TestRegistryRejectsTrailingJSONAndOversizedContent(t *testing.T) {
	fixture := loadFixtures(t)[0]
	valid := fixtureBytes(t, fixture.Document)
	if err := ValidateContract(fixture.Contract, append(valid, []byte(` {}`)...)); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
	if err := ValidateContract(fixture.Contract, make([]byte, MaxArtifactBytes+1)); err == nil {
		t.Fatal("expected artifact size rejection")
	}
}

func TestChangeSetRejectsDigestAndPatchLimitViolations(t *testing.T) {
	fixture := loadFixtures(t)[3]
	fixture.Document["patchDigest"] = "sha256:" + strings.Repeat("0", 64)
	if err := ValidateContract(fixture.Contract, fixtureBytes(t, fixture.Document)); err == nil || !strings.Contains(err.Error(), "patchDigest") {
		t.Fatalf("expected patch digest rejection, got %v", err)
	}
	fixture = loadFixtures(t)[3]
	fixture.Document["patch"] = strings.Repeat("x", MaxPatchBytes+1)
	if err := ValidateContract(fixture.Contract, fixtureBytes(t, fixture.Document)); err == nil || !strings.Contains(err.Error(), "patch must contain") {
		t.Fatalf("expected patch size rejection, got %v", err)
	}
}

func TestTestReportAndValidationSuccessPredicates(t *testing.T) {
	testReport := loadFixtures(t)[5]
	testReport.Document["workspaceClean"] = false
	if err := ValidateContract(testReport.Contract, fixtureBytes(t, testReport.Document)); err == nil || !strings.Contains(err.Error(), "passed outcome") {
		t.Fatalf("expected passing test predicate rejection, got %v", err)
	}

	validationResult := loadFixtures(t)[8]
	validationResult.Document["observedSourceRevision"] = strings.Repeat("f", 40)
	if err := ValidateContract(validationResult.Contract, fixtureBytes(t, validationResult.Document)); err == nil || !strings.Contains(err.Error(), "passed outcome") {
		t.Fatalf("expected passing validation predicate rejection, got %v", err)
	}
}
