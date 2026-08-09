package artifactcontract

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	artifactschemas "github.com/SovereignAI/schemas/artifacts/v1"
)

var contractSchemaFiles = map[string]string{
	ChangeRequestContract:      "change-request.schema.json",
	RepositoryRevisionContract: "repository-revision.schema.json",
	ImplementationPlanContract: "implementation-plan.schema.json",
	TestChangeSetContract:      "test-change-set.schema.json",
	ChangeSetContract:          "change-set.schema.json",
	PreparedCandidateContract:  "prepared-candidate.schema.json",
	TestReportContract:         "test-report.schema.json",
	CandidateRevisionContract:  "candidate-revision.schema.json",
	ImageDigestContract:        "image-digest.schema.json",
	ValidationResultContract:   "validation-result.schema.json",
	MergeRevisionContract:      "merge-revision.schema.json",
}

type SchemaRegistry struct {
	schemas map[string]SchemaDefinition
}

type SchemaDefinition struct {
	Contract string
	Name     string
	JSON     json.RawMessage
}

type schemaRegistration struct {
	Contract string
	Filename string
}

func NewSchemaRegistry() (*SchemaRegistry, error) {
	return LoadSchemaRegistry(artifactschemas.Files)
}

// Loads the common json definition ($defs), then loads and
// normalizes each contract schema to provide ready to use
// json formats
func LoadSchemaRegistry(schemaFS fs.FS) (*SchemaRegistry, error) {
	commonBytes, err := fs.ReadFile(
		schemaFS,
		"common.schema.json",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to read common schema: %w", err)
	}

	var common map[string]any
	if err := json.Unmarshal(commonBytes, &common); err != nil {
		return nil, fmt.Errorf("decode common schema failed: %w", err)
	}

	commonDefs, ok := common["$defs"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("common schema has no shared object $defs")
	}

	registry := &SchemaRegistry{schemas: make(map[string]SchemaDefinition)}

	for contract, filename := range contractSchemaFiles {
		definition, err := loadDefinition(schemaFS, commonDefs, contract, filename)
		if err != nil {
			return nil, fmt.Errorf("definition load: %w", err)
		}

		registry.schemas[contract] = definition
	}
	return registry, nil
}

func (r *SchemaRegistry) Lookup(name string, version string) (SchemaDefinition, error) {
	key := name + "/" + version
	definition, ok := r.schemas[key]
	if !ok {
		return SchemaDefinition{}, fmt.Errorf(
			"output contract %q has no registered schema",
			key,
		)
	}

	definition.JSON = append(
		json.RawMessage(nil),
		definition.JSON...,
	)

	return definition, nil
}

func loadDefinition(schemaFS fs.FS, commonDefs map[string]any, contract, filename string) (SchemaDefinition, error) {
	schemaBytes, err := fs.ReadFile(schemaFS, filename)
	if err != nil {
		return SchemaDefinition{}, err
	}

	var root map[string]any
	if err := json.Unmarshal(schemaBytes, &root); err != nil {
		return SchemaDefinition{}, fmt.Errorf("decode schema %s: %w", contract, err)
	}

	definitions, err := cloneObject(commonDefs)
	if err != nil {
		return SchemaDefinition{}, fmt.Errorf("clone common defs: %w", err)
	}

	// merge or replace common defs
	existing, found := root["$defs"]
	if !found {
		root["$defs"] = definitions
	} else {
		rootDefs, ok := existing.(map[string]any)
		if !ok {
			return SchemaDefinition{}, fmt.Errorf("schema %q has non-object $defs", contract)
		}

		for name, definition := range definitions {
			if _, collision := rootDefs[name]; collision {
				return SchemaDefinition{}, fmt.Errorf(
					"schema %q redefines $defs/%s",
					contract,
					name,
				)
			}
			rootDefs[name] = definition
		}
	}

	// remove common.schema.json#
	if err := normalizeReferences(root); err != nil {
		return SchemaDefinition{}, fmt.Errorf("normalize schema %s: %w", contract, err)
	}

	// Marshal back to read-to-apply
	resolvedBytes, err := json.Marshal(root)
	if err != nil {
		return SchemaDefinition{}, fmt.Errorf("marshal root: %w", err)
	}

	contractName, err := schemaName(contract)
	if err != nil {
		return SchemaDefinition{}, fmt.Errorf("contract name invalid: %w", err)
	}
	return SchemaDefinition{
		Contract: contract,
		Name:     contractName,
		JSON:     resolvedBytes,
	}, nil
}

func cloneObject(source map[string]any) (map[string]any, error) {
	data, err := json.Marshal(source)
	if err != nil {
		return nil, fmt.Errorf("marshal object for clone: %w", err)
	}

	var clone map[string]any
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, fmt.Errorf("decode cloned object: %w", err)
	}

	return clone, nil
}

func normalizeReferences(value any) error {
	switch current := value.(type) {
	case map[string]any:
		if reference, ok := current["$ref"].(string); ok {
			switch {
			case strings.HasPrefix(reference, "common.schema.json#"):
				current["$ref"] = strings.TrimPrefix(
					reference,
					"common.schema.json",
				)
			case strings.HasPrefix(reference, "#/"):
				// already normalized
			default:
				return fmt.Errorf(
					"unsupported external schema reference %q",
					reference,
				)
			}
		}

		for _, child := range current {
			if err := normalizeReferences(child); err != nil {
				return err
			}
		}

	case []any:
		for _, child := range current {
			if err := normalizeReferences(child); err != nil {
				return err
			}
		}
	}

	return nil
}

func schemaName(contract string) (string, error) {
	name, version, found := strings.Cut(contract, "/")
	if !found || version == "" || name == "" {
		return "", fmt.Errorf("contract must be name/version, received: %s", contract)
	}
	return name + "-" + version, nil
}
