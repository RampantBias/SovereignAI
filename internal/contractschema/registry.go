package contractschema

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

type Definition struct {
	Contract string
	Name     string
	JSON     json.RawMessage
}

type Registry interface {
	Lookup(name, version string) (Definition, error)
}

// map-backed Registry
type registry map[string]Definition

// Load builds standalone schemas by merging the supplied common $defs into
// every registered contract schema.
func Load(schemaFS, commonFS fs.FS, files map[string]string, nameSuffix string) (Registry, error) {
	commonBytes, err := fs.ReadFile(commonFS, "common.schema.json")
	if err != nil {
		return nil, fmt.Errorf("read common schema: %w", err)
	}
	var common map[string]any
	if err := json.Unmarshal(commonBytes, &common); err != nil {
		return nil, fmt.Errorf("decode common schema: %w", err)
	}
	commonDefs, ok := common["$defs"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("common schema has no object $defs")
	}

	loaded := registry{}
	for contract, filename := range files {
		definition, err := loadDefinition(schemaFS, commonDefs, contract, filename, nameSuffix)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", contract, err)
		}
		loaded[contract] = definition
	}
	return loaded, nil
}

// LoadStandalone loads schemas without merging shared definitions. It is
// intended for provider-facing contracts that must already be self-contained.
func LoadStandalone(schemaFS fs.FS, files map[string]string, nameSuffix string) (Registry, error) {
	loaded := registry{}
	for contract, filename := range files {
		definition, err := loadDefinition(schemaFS, nil, contract, filename, nameSuffix)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", contract, err)
		}
		loaded[contract] = definition
	}
	return loaded, nil
}

func (r registry) Lookup(name, version string) (Definition, error) {
	key := name + "/" + version
	definition, ok := r[key]
	if !ok {
		return Definition{}, fmt.Errorf("output contract %q has no registered schema", key)
	}
	definition.JSON = append(json.RawMessage(nil), definition.JSON...)
	return definition, nil
}

func loadDefinition(schemaFS fs.FS, commonDefs map[string]any, contract, filename, suffix string) (Definition, error) {
	data, err := fs.ReadFile(schemaFS, filename)
	if err != nil {
		return Definition{}, err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return Definition{}, fmt.Errorf("decode schema: %w", err)
	}
	if commonDefs != nil {
		definitions, err := clone(commonDefs)
		if err != nil {
			return Definition{}, err
		}
		if existing, ok := root["$defs"]; ok {
			rootDefs, ok := existing.(map[string]any)
			if !ok {
				return Definition{}, fmt.Errorf("schema has non-object $defs")
			}
			for name, definition := range definitions {
				if _, exists := rootDefs[name]; exists {
					return Definition{}, fmt.Errorf("schema redefines $defs/%s", name)
				}
				rootDefs[name] = definition
			}
		} else {
			root["$defs"] = definitions
		}
	}
	if err := normalizeReferences(root); err != nil {
		return Definition{}, err
	}
	resolved, err := json.Marshal(root)
	if err != nil {
		return Definition{}, err
	}
	name, version, ok := strings.Cut(contract, "/")
	if !ok || name == "" || version == "" {
		return Definition{}, fmt.Errorf("contract must be name/version")
	}
	return Definition{Contract: contract, Name: name + "-" + version + suffix, JSON: resolved}, nil
}

func clone(source map[string]any) (map[string]any, error) {
	data, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(data, &result)
	return result, err
}

func normalizeReferences(value any) error {
	switch current := value.(type) {
	case map[string]any:
		if reference, ok := current["$ref"].(string); ok {
			switch {
			case strings.HasPrefix(reference, "common.schema.json#"):
				current["$ref"] = strings.TrimPrefix(reference, "common.schema.json")
			case strings.HasPrefix(reference, "#/"):
			default:
				return fmt.Errorf("unsupported external schema reference %q", reference)
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
