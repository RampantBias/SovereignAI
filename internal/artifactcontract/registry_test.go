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

func fixtureForContract(t *testing.T, contract string) contractFixture {
	t.Helper()
	for _, fixture := range loadFixtures(t) {
		if fixture.Contract == contract {
			return fixture
		}
	}
	t.Fatalf("no fixture for contract %q", contract)
	return contractFixture{}
}

func TestRegistryContainsAndAcceptsEveryFrozenArtifactContract(t *testing.T) {
	fixtures := loadFixtures(t)
	registry := DefaultRegistry()
	contracts := registry.Contracts()
	if len(fixtures) != len(contracts) {
		t.Fatalf("fixture count = %d, registered contract count = %d: %v", len(fixtures), len(contracts), contracts)
	}
	seen := make(map[string]struct{}, len(fixtures))
	for _, fixture := range fixtures {
		if _, duplicate := seen[fixture.Contract]; duplicate {
			t.Fatalf("duplicate fixture for contract %q", fixture.Contract)
		}
		seen[fixture.Contract] = struct{}{}
		t.Run(fixture.Contract, func(t *testing.T) {
			if err := ValidateContract(fixture.Contract, fixtureBytes(t, fixture.Document)); err != nil {
				t.Fatalf("valid fixture rejected: %v", err)
			}
		})
	}
	for _, contract := range contracts {
		if _, ok := seen[contract]; !ok {
			t.Errorf("registered contract %q has no fixture", contract)
		}
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

func TestChangeSetsRejectDigestAndPatchLimitViolations(t *testing.T) {
	for _, contract := range []string{TestChangeSetContract, ChangeSetContract} {
		t.Run(contract, func(t *testing.T) {
			fixture := fixtureForContract(t, contract)
			fixture.Document["patchDigest"] = "sha256:" + strings.Repeat("0", 64)
			if err := ValidateContract(fixture.Contract, fixtureBytes(t, fixture.Document)); err == nil || !strings.Contains(err.Error(), "patchDigest") {
				t.Fatalf("expected patch digest rejection, got %v", err)
			}

			fixture = fixtureForContract(t, contract)
			fixture.Document["patch"] = strings.Repeat("x", MaxPatchBytes+1)
			if err := ValidateContract(fixture.Contract, fixtureBytes(t, fixture.Document)); err == nil || !strings.Contains(err.Error(), "patch must contain") {
				t.Fatalf("expected patch size rejection, got %v", err)
			}
		})
	}
}

func TestTestChangeSetRejectsProductionPaths(t *testing.T) {
	fixture := fixtureForContract(t, TestChangeSetContract)
	patch := "diff --git a/src/main.go b/src/main.go\n--- a/src/main.go\n+++ b/src/main.go\n@@ -1 +1 @@\n-old\n+new\n"
	fixture.Document["patch"] = patch
	fixture.Document["patchDigest"] = DigestBytes([]byte(patch))
	fixture.Document["files"] = []string{"src/main.go"}
	fixture.Document["byteCount"] = len([]byte(patch))
	fixture.Document["lineCounts"] = map[string]any{"added": 1, "deleted": 1}
	if err := ValidateContract(fixture.Contract, fixtureBytes(t, fixture.Document)); err == nil || !strings.Contains(err.Error(), "recognized test path") {
		t.Fatalf("expected production-path rejection, got %v", err)
	}
}

func TestTestPathClassification(t *testing.T) {
	for _, path := range []string{
		"src/main_test.go",
		"tests/calculator.go",
		"src/test_data/divide.json",
		"web/__tests__/calculator.ts",
		"python/test_calculator.py",
		"web/calculator.test.ts",
		"web/calculator.spec.js",
		"java/CalculatorTest.java",
		"dotnet/Calculator.Tests/Divide.cs",
	} {
		if !isTestPath(path) {
			t.Errorf("test path %q was not recognized", path)
		}
	}
	for _, path := range []string{"src/main.go", "web/calculator.ts", "python/calculator.py", "docs/latest.md"} {
		if isTestPath(path) {
			t.Errorf("production path %q was recognized as a test", path)
		}
	}
}

func TestTestReportAndValidationSuccessPredicates(t *testing.T) {
	testReport := fixtureForContract(t, TestReportContract)
	testReport.Document["workspaceClean"] = false
	if err := ValidateContract(testReport.Contract, fixtureBytes(t, testReport.Document)); err == nil || !strings.Contains(err.Error(), "passed outcome") {
		t.Fatalf("expected passing test predicate rejection, got %v", err)
	}

	validationResult := fixtureForContract(t, ValidationResultContract)
	validationResult.Document["observedSourceRevision"] = strings.Repeat("f", 40)
	if err := ValidateContract(validationResult.Contract, fixtureBytes(t, validationResult.Document)); err == nil || !strings.Contains(err.Error(), "passed outcome") {
		t.Fatalf("expected passing validation predicate rejection, got %v", err)
	}
}
