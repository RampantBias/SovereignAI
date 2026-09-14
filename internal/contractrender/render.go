package contractrender

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/SovereignAI/internal/contractschema"
)

// Render turns any JSON contract value into deterministic, sectioned text.
// Schema titles supply human-readable labels when available; unknown fields
// still render through the same structural fallback.
func Render(definition contractschema.Definition, document []byte) (string, error) {
	value, err := decode(document)
	if err != nil {
		return "", fmt.Errorf("decode %s document: %w", definition.Contract, err)
	}
	var schema map[string]any
	if len(definition.JSON) != 0 {
		if err := json.Unmarshal(definition.JSON, &schema); err != nil {
			return "", fmt.Errorf("decode %s schema: %w", definition.Contract, err)
		}
	}
	var output strings.Builder
	renderValue(&output, value, schema, schema, "", 0)
	return strings.TrimSpace(output.String()), nil
}

func decode(document []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing JSON data")
	}
	return value, nil
}

func renderValue(output *strings.Builder, value any, schema, root map[string]any, label string, indent int) {
	schema = resolve(schema, root)
	switch current := value.(type) {
	case map[string]any:
		if label != "" {
			line(output, indent, "["+label+"]")
		}
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childSchema := propertySchema(schema, key, root)
			renderValue(output, current[key], childSchema, root, schemaLabel(childSchema, key), indent)
		}
	case []any:
		if label != "" {
			line(output, indent, "["+label+"]")
		}
		if len(current) == 0 {
			line(output, indent, "(none)")
			return
		}
		itemSchema := resolve(childMap(schema, "items"), root)
		itemLabel := schemaLabel(itemSchema, "Item")
		for index, item := range current {
			switch scalar := item.(type) {
			case string:
				if strings.ContainsAny(scalar, "\r\n") {
					line(output, indent, fmt.Sprintf("%d.", index+1))
					renderString(output, "", scalar, indent+2)
				} else {
					line(output, indent, fmt.Sprintf("%d. %s", index+1, scalar))
				}
			case json.Number:
				line(output, indent, fmt.Sprintf("%d. %s", index+1, scalar.String()))
			case bool, nil:
				line(output, indent, fmt.Sprintf("%d. %v", index+1, scalar))
			default:
				line(output, indent, fmt.Sprintf("%d. %s", index+1, itemLabel))
				renderValue(output, item, itemSchema, root, "", indent+2)
			}
		}
	case string:
		renderString(output, label, current, indent)
	case json.Number:
		line(output, indent, scalarLine(label, current.String()))
	case bool:
		line(output, indent, scalarLine(label, fmt.Sprint(current)))
	case nil:
		line(output, indent, scalarLine(label, "null"))
	default:
		line(output, indent, scalarLine(label, fmt.Sprint(current)))
	}
}

func renderString(output *strings.Builder, label, value string, indent int) {
	if !strings.ContainsAny(value, "\r\n") {
		line(output, indent, scalarLine(label, value))
		return
	}
	line(output, indent, label+":")
	normalized := strings.ReplaceAll(value, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	for _, contentLine := range strings.Split(normalized, "\n") {
		line(output, indent+2, "| "+contentLine)
	}
}

func scalarLine(label, value string) string {
	if label == "" {
		return value
	}
	return label + ": " + value
}

func line(output *strings.Builder, indent int, value string) {
	output.WriteString(strings.Repeat(" ", indent))
	output.WriteString(value)
	output.WriteByte('\n')
}

func propertySchema(schema map[string]any, key string, root map[string]any) map[string]any {
	properties := childMap(resolve(schema, root), "properties")
	return resolve(childMap(properties, key), root)
}

func schemaLabel(schema map[string]any, fallback string) string {
	if title, ok := schema["title"].(string); ok && strings.TrimSpace(title) != "" {
		return title
	}
	return humanize(fallback)
}

func childMap(parent map[string]any, key string) map[string]any {
	if parent == nil {
		return nil
	}
	child, _ := parent[key].(map[string]any)
	return child
}

func resolve(schema, root map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	reference, _ := schema["$ref"].(string)
	if !strings.HasPrefix(reference, "#/") {
		return schema
	}
	var current any = root
	for _, part := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return schema
		}
		current, ok = object[part]
		if !ok {
			return schema
		}
	}
	resolved, ok := current.(map[string]any)
	if !ok {
		return schema
	}
	return resolved
}

func humanize(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "_", " "), "-", " ")
	var output []rune
	for index, current := range []rune(value) {
		if index > 0 && unicode.IsUpper(current) && len(output) > 0 && output[len(output)-1] != ' ' {
			output = append(output, ' ')
		}
		output = append(output, current)
	}
	words := strings.Fields(string(output))
	for index := range words {
		runes := []rune(strings.ToLower(words[index]))
		if len(runes) != 0 {
			runes[0] = unicode.ToUpper(runes[0])
		}
		words[index] = string(runes)
	}
	return strings.Join(words, " ")
}
