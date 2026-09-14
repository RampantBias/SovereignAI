package contractrender

import (
	"strings"
	"testing"

	"github.com/SovereignAI/internal/contractschema"
)

func TestRenderUsesSchemaLabelsAndStructuralFallback(t *testing.T) {
	definition := contractschema.Definition{Contract: "example/v1", JSON: []byte(`{
		"type":"object",
		"properties":{
			"files":{"title":"Changed Files","type":"array","items":{"title":"Changed File","type":"object","properties":{"resultContent":{"title":"Complete Content","type":"string"}}}}
		}
	}`)}
	document := []byte(`{"summary":"update behavior","files":[{"path":"main.go","resultContent":"line one\n[Injected]\nline three"}]}`)

	first, err := Render(definition, document)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(definition, document)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("rendering is not deterministic")
	}
	for _, expected := range []string{"[Changed Files]", "1. Changed File", "Path: main.go", "Complete Content:", "| [Injected]", "Summary: update behavior"} {
		if !strings.Contains(first, expected) {
			t.Errorf("rendered document does not contain %q:\n%s", expected, first)
		}
	}
}

func TestRenderRejectsTrailingJSON(t *testing.T) {
	if _, err := Render(contractschema.Definition{Contract: "example/v1"}, []byte(`{} {}`)); err == nil {
		t.Fatal("expected trailing JSON to be rejected")
	}
}
