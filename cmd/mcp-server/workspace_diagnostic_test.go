package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/contextrepo"
	"github.com/SovereignAI/internal/workspaceeditor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWorkspaceNoOpIsRejectedAndReportsSyntaxLocation(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	original := "package main\nfunc TestExisting(t *testing.T) {}\n"
	broken := original + "}\n"
	if err := os.WriteFile(filepath.Join(base, "main_test.go"), []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	editor := &workspaceeditor.Editor{BaseRoot: base, OverlayRoot: overlay}
	current, _ := editor.Read("main_test.go", 0)
	if _, err := editor.Write("main_test.go", broken, current.Digest); err != nil {
		t.Fatal(err)
	}
	session := connectTestMCP(t, newMcpServer(contextrepo.Repository{Root: base}, editor))
	ctx := context.Background()
	read, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_read", Arguments: map[string]any{"path": "main_test.go"}})
	if err != nil || read.IsError {
		t.Fatalf("read: %v %v", read, err)
	}
	for _, call := range []mcp.CallToolParams{
		{Name: "workspace_replace", Arguments: map[string]any{"path": "main_test.go", "oldText": broken, "newText": broken}},
		{Name: "workspace_write", Arguments: map[string]any{"path": "main_test.go", "content": strings.ReplaceAll(broken, "\n", "\r\n")}},
	} {
		result, err := session.CallTool(ctx, &call)
		if err != nil || !result.IsError {
			t.Fatalf("no-op accepted: %#v %v", result, err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		_, anchorText, ok := strings.Cut(text, "JSON string; no line numbers): ")
		if !ok {
			t.Fatal("missing exact syntax repair anchor")
		}
		var anchor string
		if err := json.Unmarshal([]byte(strings.SplitN(anchorText, "\n", 2)[0]), &anchor); err != nil {
			t.Fatal(err)
		}
		if strings.Count(broken, anchor) != 1 {
			t.Fatalf("diagnostic anchor is not unique: %q", anchor)
		}
		for _, expected := range []string{"unchanged", "no repair was made", "3: }"} {
			if !strings.Contains(text, expected) {
				t.Errorf("missing %q: %s", expected, text)
			}
		}
	}
	changes, _ := editor.Changes()
	err = validateRefinementTestPreservation(editor, changes.Files)
	if err == nil || !strings.Contains(err.Error(), "3: }") {
		t.Fatalf("completion missing source location: %v", err)
	}
	repaired, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_replace", Arguments: map[string]any{"path": "main_test.go", "oldText": "{}\n}\n", "newText": "{}\n"}})
	if err != nil || repaired.IsError {
		t.Fatalf("targeted repair failed: %#v %v", repaired, err)
	}
	file, _ := editor.Read("main_test.go", 0)
	if file.Content != original {
		t.Fatal("targeted repair did not remove only the extra brace")
	}
}

func TestSyntaxExcerptBoundsSourceWindow(t *testing.T) {
	content := "package main\n" + strings.Repeat("// padding\n", 100) + "}\n" + strings.Repeat("// trailing\n", 100)
	err := unchangedWorkspaceError(workspaceeditor.File{Path: "main_test.go", Content: content})
	if !strings.Contains(err.Error(), "102: }") || strings.Count(err.Error(), "padding") > 6 || strings.Count(err.Error(), "trailing") > 6 {
		t.Fatalf("unbounded or misplaced excerpt: %v", err)
	}
}
