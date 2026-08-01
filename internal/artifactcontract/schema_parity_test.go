package artifactcontract

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

var schemaFiles = map[string]string{
	ChangeRequestContract:      "change-request.schema.json",
	RepositoryRevisionContract: "repository-revision.schema.json",
	ImplementationPlanContract: "implementation-plan.schema.json",
	ChangeSetContract:          "change-set.schema.json",
	PreparedCandidateContract:  "prepared-candidate.schema.json",
	TestReportContract:         "test-report.schema.json",
	CandidateRevisionContract:  "candidate-revision.schema.json",
	ImageDigestContract:        "image-digest.schema.json",
	ValidationResultContract:   "validation-result.schema.json",
	MergeRevisionContract:      "merge-revision.schema.json",
}

func TestGoAndJSONSchemaFixtureParity(t *testing.T) {
	if len(schemaFiles) != len(DefaultRegistry().Contracts()) {
		t.Fatalf("schema count %d does not match registry count %d", len(schemaFiles), len(DefaultRegistry().Contracts()))
	}
	for _, fixture := range loadFixtures(t) {
		t.Run(fixture.Contract, func(t *testing.T) {
			schema := loadResolvedSchema(t, fixture.Contract)
			validBytes := fixtureBytes(t, fixture.Document)
			assertParity(t, fixture.Contract, validBytes, fixture.Document, schema, true)

			invalidDocument := cloneDocument(t, fixture.Document)
			invalidDocument["unexpected"] = true
			assertParity(t, fixture.Contract, fixtureBytes(t, invalidDocument), invalidDocument, schema, false)
		})
	}
}

func assertParity(t *testing.T, contract string, data []byte, document map[string]any, schema *jsonschema.Resolved, wantValid bool) {
	t.Helper()
	goErr := ValidateContract(contract, data)
	schemaErr := schema.Validate(document)
	if (goErr == nil) != wantValid {
		t.Fatalf("Go validation validity = %t, want %t: %v", goErr == nil, wantValid, goErr)
	}
	if (schemaErr == nil) != wantValid {
		t.Fatalf("JSON Schema validity = %t, want %t: %v", schemaErr == nil, wantValid, schemaErr)
	}
}

func loadResolvedSchema(t *testing.T, contract string) *jsonschema.Resolved {
	t.Helper()
	filename, ok := schemaFiles[contract]
	if !ok {
		t.Fatalf("no schema file for %s", contract)
	}
	root := readSchemaFile(t, filename)
	resolved, err := root.Resolve(&jsonschema.ResolveOptions{
		BaseURI: root.ID,
		Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
			if uri.Host != "sovereign.ai" || !strings.HasPrefix(uri.Path, "/schemas/artifacts/v1/") {
				return nil, fmt.Errorf("refusing unexpected schema URI %s", uri)
			}
			return readSchemaFileForLoader(filepath.Base(uri.Path))
		},
	})
	if err != nil {
		t.Fatalf("resolve schema %s: %v", filename, err)
	}
	return resolved
}

func readSchemaFile(t *testing.T, filename string) *jsonschema.Schema {
	t.Helper()
	schema, err := readSchemaFileForLoader(filename)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func readSchemaFileForLoader(filename string) (*jsonschema.Schema, error) {
	path := filepath.Join("..", "..", "schemas", "artifacts", "v1", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema %s: %w", filename, err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("decode schema %s: %w", filename, err)
	}
	return &schema, nil
}

func cloneDocument(t *testing.T, source map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
