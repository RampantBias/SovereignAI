package artifactcontract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImplementationPlanSchemaIsStandalone(t *testing.T) {
	registry, err := NewSchemaRegistry()
	if err != nil {
		t.Fatalf("NewSchemaRegistry: %v", err)
	}

	definition, err := registry.Lookup(
		"implementation-plan",
		"v1",
	)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	if strings.Contains(
		string(definition.JSON),
		"common.schema.json",
	) {
		t.Fatal("resolved schema contains external common reference")
	}

	var schema map[string]any
	if err := json.Unmarshal(definition.JSON, &schema); err != nil {
		t.Fatalf("decode resolved schema: %v", err)
	}

	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("resolved schema has no object $defs")
	}
	if _, ok := definitions["Digest"]; !ok {
		t.Fatal("resolved schema has no Digest definition")
	}

	properties := schema["properties"].(map[string]any)
	digest := properties["changeRequestDigest"].(map[string]any)

	if reference := digest["$ref"]; reference != "#/$defs/Digest" {
		t.Fatalf(
			"changeRequestDigest reference = %q",
			reference,
		)
	}
}
