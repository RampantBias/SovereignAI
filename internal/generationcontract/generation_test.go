package generationcontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/artifactcontract"
)

func TestCatalogSchemasAreFlattenedForInference(t *testing.T) {
	catalog, err := NewCatalog()
	if err != nil {
		t.Fatal(err)
	}

	for contract := range schemaFiles {
		t.Run(contract, func(t *testing.T) {
			name, version := splitContract(contract)
			binding, err := catalog.Resolve(name, version)
			if err != nil {
				t.Fatal(err)
			}

			var schema any
			if err := json.Unmarshal(binding.Schema.JSON, &schema); err != nil {
				t.Fatalf("decode generation schema: %v", err)
			}
			forbidden := map[string]struct{}{
				"$defs": {}, "$id": {}, "$ref": {}, "$schema": {},
				"pattern": {}, "title": {}, "uniqueItems": {},
			}
			if path, keyword, found := findSchemaKeyword(schema, "$", forbidden); found {
				t.Fatalf("generation schema contains provider-facing keyword %q at %s", keyword, path)
			}
		})
	}
}

func findSchemaKeyword(value any, path string, forbidden map[string]struct{}) (string, string, bool) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			childPath := path + "." + key
			if key == "properties" {
				properties, ok := child.(map[string]any)
				if !ok {
					return childPath, key, true
				}
				for propertyName, propertySchema := range properties {
					propertyPath := childPath + "." + propertyName
					if foundPath, keyword, found := findSchemaKeyword(propertySchema, propertyPath, forbidden); found {
						return foundPath, keyword, true
					}
				}
				continue
			}
			if _, found := forbidden[key]; found {
				return childPath, key, true
			}
			if foundPath, keyword, found := findSchemaKeyword(child, childPath, forbidden); found {
				return foundPath, keyword, true
			}
		}
	case []any:
		for index, child := range current {
			childPath := fmt.Sprintf("%s[%d]", path, index)
			if foundPath, keyword, found := findSchemaKeyword(child, childPath, forbidden); found {
				return foundPath, keyword, true
			}
		}
	}
	return "", "", false
}

func TestChangeSetBindingFinalizesRuntimeFields(t *testing.T) {
	catalog, err := NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	revision := []byte(`{"repositoryURL":"https://git.example.test/calculator.git","requestedRevision":"main","resolvedCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","utilityOperation":{"namespace":"workflow","name":"initialize-001","uid":"operation-uid"}}`)
	sources := []SourceArtifact{
		{Contract: artifactcontract.ChangeRequestContract, Digest: artifactcontract.DigestBytes([]byte("change"))},
		{Contract: artifactcontract.ImplementationPlanContract, Digest: artifactcontract.DigestBytes([]byte("plan"))},
		{Contract: artifactcontract.RepositoryRevisionContract, Digest: artifactcontract.DigestBytes(revision), Content: revision},
	}

	tests := []struct {
		contract string
		path     string
	}{
		{contract: artifactcontract.TestChangeSetContract, path: "main_test.go"},
		{contract: artifactcontract.ChangeSetContract, path: "main.go"},
	}
	for _, test := range tests {
		t.Run(test.contract, func(t *testing.T) {
			patchLines := []string{
				"diff --git a/" + test.path + " b/" + test.path,
				"--- a/" + test.path,
				"+++ b/" + test.path,
				"@@ -1 +1 @@",
				"-old",
				"+new",
			}
			patch := strings.Join(patchLines, "\n") + "\n"
			candidate, err := json.Marshal(map[string]any{"summary": "change", "patchLines": patchLines})
			if err != nil {
				t.Fatal(err)
			}
			name, version := splitContract(test.contract)
			binding, err := catalog.Resolve(name, version)
			if err != nil {
				t.Fatal(err)
			}
			content, err := binding.Finalize(candidate, sources)
			if err != nil {
				t.Fatal(err)
			}
			if err := artifactcontract.ValidateContract(test.contract, content); err != nil {
				t.Fatalf("final artifact is invalid: %v", err)
			}
			var change artifactcontract.ChangeSet
			if err := json.Unmarshal(content, &change); err != nil {
				t.Fatal(err)
			}
			if change.Format != "unified-diff" || change.PatchDigest != artifactcontract.DigestBytes([]byte(patch)) ||
				len(change.Files) != 1 || change.Files[0] != test.path || change.LineCounts.Added != 1 || change.LineCounts.Deleted != 1 {
				t.Fatalf("derived patch fields are incorrect: %#v", change)
			}
		})
	}
}

func TestJoinPatchLinesRejectsInvalidLineEncoding(t *testing.T) {
	valid := []string{"diff --git a/main.go b/main.go", "--- a/main.go", "+++ b/main.go", "@@ -1 +1 @@", "-old", "+new"}
	for name, mutate := range map[string]func([]string) []string{
		"too few": func(lines []string) []string { return lines[:4] },
		"empty": func(lines []string) []string {
			lines[4] = ""
			return lines
		},
		"embedded newline": func(lines []string) []string {
			lines[4] = "-old\n+new"
			return lines
		},
	} {
		t.Run(name, func(t *testing.T) {
			lines := mutate(append([]string(nil), valid...))
			if _, err := joinPatchLines(lines); err == nil {
				t.Fatal("invalid patch lines accepted")
			}
		})
	}
}

func TestDecodeNormalizesRawJSONControlCharactersInsideStrings(t *testing.T) {
	data := []byte("{\n\t\"raw\":\"a\tb\",\"escaped\":\"c\\td\"}")
	var decoded struct {
		Raw     string `json:"raw"`
		Escaped string `json:"escaped"`
	}
	if err := decode(data, &decoded); err != nil {
		t.Fatalf("decode normalized response: %v", err)
	}
	if decoded.Raw != "a\tb" || decoded.Escaped != "c\td" {
		t.Fatalf("decoded values changed: %#v", decoded)
	}
}

func TestNormalizeJSONControlCharactersLeavesValidJSONUnchanged(t *testing.T) {
	data := []byte("{\n\t\"value\":\"already\\tescaped\"\n}")
	normalized := normalizeJSONControlCharacters(data)
	if !bytes.Equal(normalized, data) {
		t.Fatalf("valid JSON changed from %q to %q", data, normalized)
	}
}

func splitContract(contract string) (string, string) {
	for index, char := range contract {
		if char == '/' {
			return contract[:index], contract[index+1:]
		}
	}
	return contract, ""
}
