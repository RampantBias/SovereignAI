package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/contextrepo"
	"github.com/SovereignAI/internal/workspaceeditor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWorkspaceToolsEditOverlayAndDeriveChanges(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "main.go"), []byte("package main\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	server := newServer(contextrepo.Repository{Root: base, Revision: "abc"}, &workspaceeditor.Editor{BaseRoot: base, OverlayRoot: overlay})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	read, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_read", Arguments: map[string]any{"path": "main.go"}})
	if err != nil {
		t.Fatal(err)
	}
	file := decodeStructured[workspaceeditor.File](t, read.StructuredContent)
	written, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_write", Arguments: map[string]any{
		"path": "main.go", "content": "package changed\r\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	updated := decodeStructured[workspaceeditor.File](t, written.StructuredContent)
	if file.Digest == updated.Digest || updated.Content != "package changed\n" {
		t.Fatalf("trusted write did not normalize content or update digest: %#v", updated)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_changes", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	changes := decodeStructured[workspaceeditor.Changes](t, result.StructuredContent)
	if len(changes.Files) != 1 || changes.Files[0] != "main.go" || changes.Patch == "" {
		t.Fatalf("unexpected changes: %#v", changes)
	}
	baseContent, _ := os.ReadFile(filepath.Join(base, "main.go"))
	if string(baseContent) != "package main\n" {
		t.Fatal("workspace tool mutated the base checkout")
	}
}

func TestWorkspaceMutationRequiresSuccessfulRead(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "main.go"), []byte("package main\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	server := newServer(contextrepo.Repository{Root: base}, &workspaceeditor.Editor{BaseRoot: base, OverlayRoot: overlay})
	session := connectTestMCP(t, server)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agentcontract.CapabilityWorkspaceWrite,
		Arguments: map[string]any{
			"path": "main.go", "content": "package changed\n",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("workspace write without inspection succeeded: %#v", result.StructuredContent)
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agentcontract.CapabilityWorkspaceRead, Arguments: map[string]any{"path": "main.go"},
	}); err != nil {
		t.Fatal(err)
	}
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agentcontract.CapabilityWorkspaceWrite,
		Arguments: map[string]any{
			"path": "main.go", "content": "package changed\n",
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("workspace write after inspection failed: result=%#v err=%v", result, err)
	}
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agentcontract.CapabilityWorkspaceWrite,
		Arguments: map[string]any{
			"path": "guessed.go", "content": "package guessed\n",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("workspace_write created an absent guessed path")
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agentcontract.CapabilityWorkspaceTree, Arguments: map[string]any{"path": ".", "maxEntries": 5000},
	}); err != nil {
		t.Fatal(err)
	}
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agentcontract.CapabilityWorkspaceCreate,
		Arguments: map[string]any{
			"path": "created.go", "content": "package created\n",
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("explicit workspace_create failed: result=%#v err=%v", result, err)
	}
}

func TestWorkspaceReadMissingPathSuggestsTreePaths(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "main.go"), []byte("package main\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	server := newServer(contextrepo.Repository{Root: base}, &workspaceeditor.Editor{BaseRoot: base, OverlayRoot: overlay})
	session := connectTestMCP(t, server)
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agentcontract.CapabilityWorkspaceTree, Arguments: map[string]any{"path": ".", "maxEntries": 5000},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agentcontract.CapabilityWorkspaceRead, Arguments: map[string]any{"path": "src/main.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(string(content), "main.go") {
		t.Fatalf("missing path did not return a tree-grounded suggestion: %#v", result.Content)
	}
}

func TestWorkspaceWriteRecoversOnlyAuthorizedInspectedPath(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	for path, content := range map[string]string{
		"main.go":      "package main\n",
		"main_test.go": "package main\n\nfunc TestExisting(t *testing.T) {}\n",
	} {
		if err := os.WriteFile(filepath.Join(base, path), []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	input := agentcontract.Input{
		Capabilities: []string{
			agentcontract.CapabilityWorkspaceRead,
			agentcontract.CapabilityWorkspaceWrite,
			agentcontract.CapabilityWorkspaceTree,
			agentcontract.CapabilityAgentComplete,
		},
		Outputs: []agentcontract.OutputObligation{{Name: "test-change-set", Version: "v1", Required: true, MediaType: "application/json"}},
	}
	server := newServer(contextrepo.Repository{Root: base}, &workspaceeditor.Editor{BaseRoot: base, OverlayRoot: overlay}, &input)
	session := connectTestMCP(t, server)
	for _, path := range []string{"main.go", "main_test.go"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: agentcontract.CapabilityWorkspaceRead, Arguments: map[string]any{"path": path},
		})
		if err != nil || result.IsError {
			t.Fatalf("read %s failed: result=%#v err=%v", path, result, err)
		}
	}
	written, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agentcontract.CapabilityWorkspaceWrite,
		Arguments: map[string]any{
			"content": "package main\n\nfunc TestRecovered(t *testing.T) {}\n",
		},
	})
	if err != nil || written.IsError {
		t.Fatalf("path recovery failed: result=%#v err=%v", written, err)
	}
	file := decodeStructured[workspaceeditor.File](t, written.StructuredContent)
	if file.Path != "main_test.go" || !strings.Contains(file.Content, "TestRecovered") {
		t.Fatalf("write recovered the wrong path: %#v", file)
	}
}

func TestAgentToolsWriteCandidateAndCompleteWithItsDigest(t *testing.T) {
	base, overlay, staging, inputs := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "main.go"), []byte("package main\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	changeRequest := []byte(`{"summary":"Add division","description":"Add division","acceptanceCriteria":["84 / 2 returns 42"],"repositoryURL":"https://git.example.test/calculator.git","sourceCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	repositoryRevision := []byte(`{"repositoryURL":"https://git.example.test/calculator.git","requestedRevision":"main","resolvedCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","utilityOperation":{"namespace":"workflow","name":"initialize-001","uid":"utility-operation-uid"}}`)
	changeRequestPath := filepath.Join(inputs, "change-request.json")
	repositoryRevisionPath := filepath.Join(inputs, "repository-revision.json")
	if err := os.WriteFile(changeRequestPath, changeRequest, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repositoryRevisionPath, repositoryRevision, 0o640); err != nil {
		t.Fatal(err)
	}
	input := agentcontract.Input{
		Outputs: []agentcontract.OutputObligation{{Name: "implementation-plan", Version: "v1", Required: true, MediaType: "application/json"}},
		Inputs: []agentcontract.ArtifactInput{
			{Contract: artifactcontract.ChangeRequestContract, Digest: artifactcontract.DigestBytes(changeRequest), Path: changeRequestPath},
			{Contract: artifactcontract.RepositoryRevisionContract, Digest: artifactcontract.DigestBytes(repositoryRevision), Path: repositoryRevisionPath},
		},
		Capabilities: []string{
			agentcontract.CapabilityWorkspaceRead,
			agentcontract.CapabilityWorkspaceTree,
			agentcontract.CapabilityCandidateWrite,
			agentcontract.CapabilityAgentComplete,
		},
		StagingPath: staging,
	}
	server := newServer(
		contextrepo.Repository{Root: base, Revision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		&workspaceeditor.Editor{BaseRoot: base, OverlayRoot: overlay},
		&input,
	)
	session := connectTestMCP(t, server)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	for _, expected := range []string{
		agentcontract.CapabilityWorkspaceRead,
		agentcontract.CapabilityWorkspaceTree,
		agentcontract.CapabilityCandidateWrite,
		agentcontract.CapabilityAgentComplete,
	} {
		if !slices.Contains(names, expected) {
			t.Errorf("agent MCP tools %v do not contain %q", names, expected)
		}
	}
	if slices.Contains(names, agentcontract.CapabilityWorkspaceWrite) {
		t.Fatalf("candidate-only agent unexpectedly has workspace_write: %v", names)
	}
	tree, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agentcontract.CapabilityWorkspaceTree, Arguments: map[string]any{"path": ".", "maxEntries": 5000},
	})
	if err != nil || tree.IsError {
		t.Fatalf("workspace_tree failed: result=%#v err=%v", tree, err)
	}
	listing := decodeStructured[TreeOutput](t, tree.StructuredContent)
	if listing.Root != "." || listing.TotalEntries != 1 || listing.Truncated || !slices.Equal(listing.Entries, []string{"main.go"}) {
		t.Fatalf("workspace_tree output = %#v", listing)
	}

	candidate := map[string]any{
		"summary":             "Implement division",
		"affectedPaths":       []any{map[string]any{"path": "main.go", "action": "modify"}},
		"implementationSteps": []string{"Add division"},
		"testStrategy":        []string{"Run go test ./..."},
		"acceptanceMapping":   []any{map[string]any{"criterionIndex": 1, "verification": "Exercise division"}},
		"risks":               []string{}, "assumptions": []string{},
	}
	written, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      agentcontract.CapabilityCandidateWrite,
		Arguments: map[string]any{"content": candidate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if written.IsError {
		t.Fatalf("candidate_write failed: %#v", written.Content)
	}
	evidence := decodeStructured[artifactcontract.CandidateEvidence](t, written.StructuredContent)
	completed, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      agentcontract.CapabilityAgentComplete,
		Arguments: map[string]any{"summary": "Implemented the plan candidate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.IsError {
		t.Fatalf("agent_complete failed: %#v", completed.Content)
	}
	completion := decodeStructured[agentcontract.AgentCompletion](t, completed.StructuredContent)
	if completion.Summary != "Implemented the plan candidate" || completion.EvidenceDigest != evidence.Digest {
		t.Fatalf("completion = %#v, candidate = %#v", completion, evidence)
	}
}

func connectTestMCP(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func decodeStructured[T any](t *testing.T, value any) T {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
