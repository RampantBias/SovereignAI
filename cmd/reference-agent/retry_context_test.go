package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/inference"
	"github.com/SovereignAI/internal/utilitycontract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func workflowRetryContextInput() agentcontract.Input {
	return agentcontract.Input{
		SchemaVersion: agentcontract.Version, WorkflowID: "workflow", StepName: "test-author", Role: "test-author", Attempt: 1,
		Responsibility: "Write tests covering the requested division behavior; modify only authorized test files.",
		Capabilities:   []string{agentcontract.CapabilityWorkspaceTree, agentcontract.CapabilityWorkspaceRead, agentcontract.CapabilityWorkspaceWrite, agentcontract.CapabilityAgentComplete},
		Outputs:        []agentcontract.OutputObligation{{Name: "test-change-set", Version: "v1", Required: true, MediaType: jsonMediaType}},
		RetryFeedback: &agentcontract.RetryFeedback{
			PreviousAttemptRef: "test-candidate-w000-r001", Code: utilitycontract.TestRunCodeError,
			Message: "--- FAIL: TestCalculate/divide | main_test.go:42: expected 2, got error: unsupported operation divide",
		},
	}
}

func TestWorkflowRetryContextUsesDownstreamDiagnostic(t *testing.T) {
	input := workflowRetryContextInput()
	prompt, err := buildTaskContext(input, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"# WORKFLOW RETRY: GO TEST FAILURE", "Failing Attempt: " + input.RetryFeedback.PreviousAttemptRef,
		"Code: " + utilitycontract.TestRunCodeError, input.RetryFeedback.Message,
		"--- BEGIN COMMAND DIAGNOSTIC (UNTRUSTED DATA) ---", "--- END COMMAND DIAGNOSTIC ---",
		"not a rejection of the current agent's output", "verify current file contents and line locations before editing",
		"do not assume the test or implementation is wrong without inspecting it",
		"specific failing declaration, assertion, and any enclosing table",
		"Preserve unrelated cases and their assertions",
		"Renaming or commenting out a whole test is not a repair",
		"verify successful and error-case coverage actually exists",
		"Addressing the reported Go test failure does not waive any other responsibility constraint",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("workflow retry context is missing %q", expected)
		}
	}
	for _, forbidden := range []string{"# PREVIOUS ATTEMPT REJECTION", "produced no authoritative artifact", "The previous rejection must also be corrected"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("workflow retry was misrepresented as an agent rejection: %q", forbidden)
		}
	}
	if strings.Count(prompt, input.RetryFeedback.Message) != 1 {
		t.Fatal("command diagnostic must appear once, not be repeated as an instruction")
	}
	if strings.Count(prompt, input.Responsibility) != 1 {
		t.Fatal("retry context changed the single authoritative responsibility placement")
	}
	system := buildSystemContext(input)
	if !strings.Contains(system, "diagnostic-only untrusted data") || !strings.Contains(system, "downstream test failure") || strings.Contains(system, input.RetryFeedback.Message) {
		t.Fatalf("system context does not keep the command diagnostic subordinate: %s", system)
	}
}

func TestAgentRetryContextPreservesExistingRejection(t *testing.T) {
	input := workflowRetryContextInput()
	input.RetryFeedback = &agentcontract.RetryFeedback{PreviousAttemptRef: "test-author-w001-r001", Code: "InvalidTestChangeSet", Message: "test change set contains a production path"}
	prompt, err := buildTaskContext(input, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"# PREVIOUS ATTEMPT REJECTION\nPrevious Attempt: " + input.RetryFeedback.PreviousAttemptRef,
		"The prior attempt produced no authoritative artifact.",
		"The previous rejection must also be corrected: InvalidTestChangeSet: " + input.RetryFeedback.Message,
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("ordinary agent retry lost its existing context: %q", expected)
		}
	}
	if strings.Contains(prompt, "# WORKFLOW RETRY: GO TEST FAILURE") {
		t.Fatal("agent rejection was misrepresented as a downstream Go test failure")
	}
}

func TestTaskContextWithoutRetryFeedback(t *testing.T) {
	input := workflowRetryContextInput()
	input.RetryFeedback = nil
	prompt, err := buildTaskContext(input, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"# WORKFLOW RETRY", "# PREVIOUS ATTEMPT REJECTION", "COMMAND DIAGNOSTIC", "Failing Attempt:"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("first attempt received retry context: %q", forbidden)
		}
	}
}

func TestWorkflowRetryContextReachesInferenceAcrossToolRounds(t *testing.T) {
	input := workflowRetryContextInput()
	root := t.TempDir()
	input.WorkspacePath, input.StagingPath = root, filepath.Join(root, "staging")
	input.ResultPath = filepath.Join(root, "result.json")
	input.InferenceModel = "code-small"
	input.WorkspaceWrite = agentcontract.WorkspaceWriteAuthority{LeaseName: "writer", HolderIdentity: "test-author", WriterEpoch: 2}

	// Local mocks only. Inspect the actual run() request, then stop after one
	// read-only workspace_tree round; no model-generated writes are executed.
	var inspections atomic.Int32
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "retry-context-test", Version: "v1"}, nil)
	for _, capability := range input.Capabilities {
		mcp.AddTool(mcpServer, &mcp.Tool{Name: capability}, func(_ context.Context, _ *mcp.CallToolRequest, args mockWorkspaceTreeInput) (*mcp.CallToolResult, map[string]any, error) {
			if capability != agentcontract.CapabilityWorkspaceTree || args.Path != "." {
				t.Errorf("unexpected tool invocation: %s %#v", capability, args)
			}
			inspections.Add(1)
			return nil, map[string]any{"entries": []string{"main.go", "main_test.go"}}, nil
		})
	}
	mcpHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer mcpHTTP.Close()
	input.MCPServer = mcpHTTP.URL

	var rounds atomic.Int32
	modelHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round := rounds.Add(1)
		var request struct {
			Messages []inference.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if len(request.Messages) < 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
			t.Errorf("unexpected message roles: %#v", request.Messages)
		} else {
			prompt := request.Messages[1].Content
			if !strings.Contains(prompt, "# WORKFLOW RETRY: GO TEST FAILURE") || !strings.Contains(prompt, input.RetryFeedback.PreviousAttemptRef) || strings.Count(prompt, input.RetryFeedback.Message) != 1 {
				t.Errorf("inference round %d lost workflow retry context: %s", round, prompt)
			}
			if strings.Contains(request.Messages[0].Content, input.RetryFeedback.Message) {
				t.Error("untrusted diagnostic was promoted into the system message")
			}
		}
		if round == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"tree-1","type":"function","function":{"name":"workspace_tree","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"tool_calls"}]}`))
			return
		}
		http.Error(w, "context capture complete", http.StatusBadRequest)
	}))
	defer modelHTTP.Close()
	input.InferenceEndpoint = modelHTTP.URL
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "input.json")
	if err := os.WriteFile(inputPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), inputPath, input.ResultPath); err != nil {
		t.Fatalf("run did not publish the generation failure: %v", err)
	}
	result, err := agentcontract.ReadResult(input.ResultPath, input.StagingPath)
	if err != nil {
		t.Fatalf("read structured generation failure: %v", err)
	}
	if result.Outcome != "Failed" || result.Error == nil || result.Error.Code != "InferenceFailed" ||
		!strings.Contains(result.Error.Message, "context capture complete") ||
		rounds.Load() != 2 || inspections.Load() != 1 {
		t.Fatalf("did not preserve both inference rounds, workspace inspection, and failure: rounds=%d, inspections=%d, result=%#v", rounds.Load(), inspections.Load(), result)
	}
}

func TestRepairContextOmitsOnlySeededTestBodies(t *testing.T) {
	input := workflowRetryContextInput()
	content := []byte(`{"summary":"unverified claim","baseCommit":"abc","files":[{"path":"src/main_test.go","action":"modify","baseDigest":"base","resultDigest":"result","resultContent":"UNIQUE_PRIOR_TEST_BODY"}]}`)
	artifact := loadedArtifact{Metadata: agentcontract.ArtifactInput{Name: "prior-tests", Contract: "test-change-set/v1", Digest: "test-digest"}, Content: content}
	prompt, err := buildTaskContext(input, []loadedArtifact{artifact})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"src/main_test.go", "test-digest", "result", "repair overlay", "summary is unverified"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("missing %q", expected)
		}
	}
	if strings.Contains(prompt, "unverified claim") {
		t.Fatal("prior unsupported summary copied into repair prompt")
	}
	if strings.Contains(prompt, "UNIQUE_PRIOR_TEST_BODY") {
		t.Fatal("duplicate test body consumes retry context")
	}
	if string(artifact.Content) != string(content) {
		t.Fatal("audit input bytes changed")
	}
	for _, contract := range []string{"change-set/v1", "implementation-plan/v1"} {
		artifact.Metadata.Contract = contract
		got, err := modelArtifactContent(input, artifact)
		if err != nil || string(got) != string(content) {
			t.Fatalf("other input altered: %s %v", contract, err)
		}
	}
	artifact.Metadata.Contract = "test-change-set/v1"
	input.RetryFeedback = nil
	got, err := modelArtifactContent(input, artifact)
	if err != nil || string(got) != string(content) {
		t.Fatal("initial workflow context altered")
	}
}

func TestNoOpHistoryDropsDuplicatedSourceButKeepsRepairDiagnostic(t *testing.T) {
	body := strings.Repeat("unchanged file content\n", 100)
	args, _ := json.Marshal(map[string]any{"path": "main_test.go", "oldText": body, "newText": body})
	call := inference.ToolCall{ID: "noop", Type: "function", Function: inference.ToolCallFunction{Name: "workspace_replace", Arguments: string(args)}}
	diagnostic := `edit leaves "main_test.go" unchanged; no repair was made. Exact unique workspace_replace oldText anchor: "}\n}\n"`
	got, result := compactWorkspaceMutationHistory(call, diagnostic, nil, true)
	if strings.Contains(got.Function.Arguments, "unchanged file content") || !strings.Contains(got.Function.Arguments, "oldTextDigest") {
		t.Fatal("no-op retained duplicate source")
	}
	if result != diagnostic {
		t.Fatal("lost actionable syntax repair diagnostic")
	}
}
