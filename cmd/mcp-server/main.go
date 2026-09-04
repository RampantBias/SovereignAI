package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/contextrepo"
	"github.com/SovereignAI/internal/workspaceeditor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type TreeInput struct {
	Path       string `json:"path,omitempty"`
	MaxEntries int    `json:"maxEntries,omitempty"`
}
type TreeOutput struct {
	Root         string   `json:"root"`
	Entries      []string `json:"entries"`
	TotalEntries int      `json:"totalEntries"`
	Truncated    bool     `json:"truncated"`
}
type ReadInput struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"maxBytes,omitempty"`
}
type ReadOutput struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Digest   string `json:"digest,omitempty"`
	Source   string `json:"source,omitempty"`
	Revision string `json:"revision"`
}
type SearchInput struct {
	Path       string `json:"path,omitempty"`
	Query      string `json:"query"`
	MaxResults int    `json:"maxResults,omitempty"`
}
type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}
type SearchOutput struct {
	Matches  []SearchMatch `json:"matches"`
	Revision string        `json:"revision"`
}
type MetadataInput struct{}
type MetadataOutput struct {
	Revision string `json:"revision"`
}
type BundleInput struct {
	Paths []string `json:"paths"`
}
type BundleOutput struct {
	Bundle contextrepo.Bundle `json:"bundle"`
}
type WriteInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type WriteOutput struct {
	Path      string `json:"path"`
	Digest    string `json:"digest"`
	ByteCount int    `json:"byteCount"`
	Changed   bool   `json:"changed"`
}
type UpdateInput struct {
	Path    string `json:"path,omitempty"`
	Content string `json:"content"`
}
type ReplaceInput struct {
	Path                string `json:"path"`
	OldText             string `json:"oldText"`
	NewText             string `json:"newText"`
	ExpectedOccurrences int    `json:"expectedOccurrences,omitempty"`
}
type DeleteInput struct {
	Path string `json:"path"`
}
type DeleteOutput struct {
	Path    string `json:"path"`
	Deleted bool   `json:"deleted"`
}
type ChangesInput struct{}

type CandidateWriteInput struct {
	Content json.RawMessage `json:"content"`
}

type AgentCompleteInput struct {
	Summary string `json:"summary"`
}

type AgentCompleteOutput = agentcontract.AgentCompletion

type workspaceToolState struct {
	mu           sync.Mutex
	readPaths    map[string]struct{}
	treePaths    map[string]struct{}
	treeObserved bool
	treeComplete bool
}

func newWorkspaceToolState() *workspaceToolState {
	return &workspaceToolState{readPaths: map[string]struct{}{}, treePaths: map[string]struct{}{}}
}

func (s *workspaceToolState) recordRead(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readPaths[path] = struct{}{}
}

func (s *workspaceToolState) recordTree(root string, listing workspaceeditor.TreeListing) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if root == "" || root == "." {
		s.treeObserved = true
		s.treeComplete = s.treeComplete || !listing.Truncated
	}
	for _, path := range listing.Entries {
		s.treePaths[path] = struct{}{}
	}
}

func (s *workspaceToolState) workspacePathEvidence() (paths []string, observed, complete bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths = make([]string, 0, len(s.treePaths))
	for path := range s.treePaths {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, s.treeObserved, s.treeComplete
}

func (s *workspaceToolState) requireUpdate(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.readPaths[path]; !ok {
		return fmt.Errorf("workspace_read must succeed for exact existing path %q before it can be changed", path)
	}
	return nil
}

func (s *workspaceToolState) inspectedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := make([]string, 0, len(s.readPaths))
	for path := range s.readPaths {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

func (s *workspaceToolState) requireCreate(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.treeObserved {
		return fmt.Errorf("workspace_tree must succeed before new path %q can be created", path)
	}
	if _, exists := s.treePaths[path]; exists {
		return fmt.Errorf("workspace path %q already exists; use workspace_write after workspace_read", path)
	}
	return nil
}

func (s *workspaceToolState) missingPathError(path, retryTool string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidates := make(map[string]struct{}, len(s.readPaths)+len(s.treePaths))
	for candidate := range s.readPaths {
		candidates[candidate] = struct{}{}
	}
	for candidate := range s.treePaths {
		candidates[candidate] = struct{}{}
	}
	nearby := make([]string, 0, 5)
	base := path
	if separator := strings.LastIndexAny(base, "/\\"); separator >= 0 {
		base = base[separator+1:]
	}
	normalizedBase := strings.ToLower(base)
	for candidate := range candidates {
		normalizedCandidate := strings.ToLower(candidate)
		if normalizedBase == "" || strings.Contains(normalizedCandidate, normalizedBase) || strings.Contains(normalizedBase, normalizedCandidate) {
			nearby = append(nearby, candidate)
		}
	}
	slices.Sort(nearby)
	if len(nearby) > 5 {
		nearby = nearby[:5]
	}
	if len(nearby) == 0 {
		if retryTool == agentcontract.CapabilityWorkspaceRead {
			return fmt.Errorf("workspace path %q does not exist; call workspace_search for text known to be in the intended file, then retry workspace_read with the exact returned path", path)
		}
		return fmt.Errorf("workspace path %q does not exist; call workspace_search for text known to be in the intended file, call workspace_read with the exact returned path, then retry %s with that path", path, retryTool)
	}
	if retryTool == agentcontract.CapabilityWorkspaceRead {
		return fmt.Errorf("workspace path %q does not exist; suggested exact workspace paths: %s; retry workspace_read with the intended path, or call workspace_search for text known to be in the file", path, strings.Join(nearby, ", "))
	}
	return fmt.Errorf("workspace path %q does not exist; suggested exact workspace paths: %s; call workspace_read with the intended path, then retry %s; if none match, call workspace_search for text known to be in the file", path, strings.Join(nearby, ", "), retryTool)
}

func main() {
	repository := contextrepo.Repository{
		Root:      env("SOVEREIGN_REPOSITORY_ROOT", "/repository"),
		BundleDir: env("SOVEREIGN_CONTEXT_BUNDLE_DIR", "/workspace/context"),
		Revision:  env("SOVEREIGN_REPOSITORY_REVISION", "unknown"),
	}
	var editor *workspaceeditor.Editor
	if overlay := os.Getenv("SOVEREIGN_WORKSPACE_OVERLAY_ROOT"); overlay != "" {
		configured := &workspaceeditor.Editor{BaseRoot: repository.Root, OverlayRoot: overlay}
		if err := configured.Validate(); err != nil {
			log.Fatalf("invalid workspace editor configuration: %v", err)
		}
		editor = configured
	}
	var runtimeInput *agentcontract.Input
	if inputPath := os.Getenv("SOVEREIGN_AGENT_INPUT"); inputPath != "" {
		input, err := agentcontract.ReadInput(inputPath)
		if err != nil {
			log.Fatalf("read agent input: %v", err)
		}
		runtimeInput = &input
	}
	server := newMcpServer(repository, editor, runtimeInput)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, SessionTimeout: 15 * time.Minute})
	mux := http.NewServeMux()
	mux.Handle("/mcp", requireAttemptIdentity(handler))
	mux.HandleFunc("/healthz", func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusOK) })
	address := ":8080"
	if editor != nil {
		address = "127.0.0.1:8080"
	}
	httpServer := &http.Server{Addr: env("SOVEREIGN_MCP_ADDRESS", address), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("Sovereign MCP server listening on %s", httpServer.Addr)
	log.Fatal(httpServer.ListenAndServe())
}

func newMcpServer(repository contextrepo.Repository, editor *workspaceeditor.Editor, runtimeInputs ...*agentcontract.Input) *mcp.Server {
	var runtimeInput *agentcontract.Input
	if len(runtimeInputs) > 0 {
		runtimeInput = runtimeInputs[0]
	}
	allowed := func(capability string) bool {
		return runtimeInput == nil || slices.Contains(runtimeInput.Capabilities, capability)
	}
	workspaceState := newWorkspaceToolState()
	var mutationPolicy *artifactcontract.WorkspaceMutationPolicy
	if runtimeInput != nil && len(runtimeInput.Outputs) == 1 {
		contract := runtimeInput.Outputs[0].Name + "/" + runtimeInput.Outputs[0].Version
		if contract == artifactcontract.TestChangeSetContract || contract == artifactcontract.ChangeSetContract {
			sources, err := loadMaterializationSources(runtimeInput.Inputs)
			if err != nil {
				panic(fmt.Sprintf("load workspace mutation policy sources: %v", err))
			}
			mutationPolicy, err = artifactcontract.NewWorkspaceMutationPolicy(contract, sources)
			if err != nil {
				panic(fmt.Sprintf("build workspace mutation policy: %v", err))
			}
		}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "sovereign-context", Version: "v1alpha1"}, nil)
	if runtimeInput == nil {
		registerRepositoryTools(server, repository, editor)
	}
	if editor != nil {
		registerWorkspaceTools(server, repository, editor, workspaceState, mutationPolicy, allowed)
	}
	if runtimeInput != nil {
		if err := registerAgentTools(server, editor, workspaceState, *runtimeInput, allowed); err != nil {
			panic(fmt.Sprintf("register agent MCP tools: %v", err))
		}
	}
	return server
}

func registerRepositoryTools(server *mcp.Server, repository contextrepo.Repository, editor *workspaceeditor.Editor) {
	mcp.AddTool(server, &mcp.Tool{Name: "repository_tree", Description: "List bounded repository paths"}, func(_ context.Context, _ *mcp.CallToolRequest, input TreeInput) (*mcp.CallToolResult, TreeOutput, error) {
		entries, err := repository.Tree(input.Path, input.MaxEntries)
		output := TreeOutput{Root: input.Path, Entries: entries, TotalEntries: len(entries)}
		if editor != nil {
			listing, listingErr := editor.TreeListing(input.Path, input.MaxEntries)
			output = treeOutput(input.Path, listing)
			err = listingErr
		}
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "repository_read", Description: "Read one bounded repository file"}, func(_ context.Context, _ *mcp.CallToolRequest, input ReadInput) (*mcp.CallToolResult, ReadOutput, error) {
		if editor != nil {
			file, err := editor.Read(input.Path, input.MaxBytes)
			return nil, ReadOutput{Path: file.Path, Content: file.Content, Digest: file.Digest, Source: file.Source, Revision: repository.Revision}, err
		}
		content, err := repository.Read(input.Path, input.MaxBytes)
		return nil, ReadOutput{Path: input.Path, Content: content, Revision: repository.Revision}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "repository_search", Description: "Search repository text with bounded results"}, func(_ context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		if editor != nil {
			matches, err := editor.Search(input.Path, input.Query, input.MaxResults)
			return nil, SearchOutput{Matches: fromEditorMatches(matches), Revision: repository.Revision}, err
		}
		matches, err := repository.Search(input.Path, input.Query, input.MaxResults)
		return nil, SearchOutput{Matches: fromRepositoryMatches(matches), Revision: repository.Revision}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "repository_metadata", Description: "Return immutable repository metadata"}, func(_ context.Context, _ *mcp.CallToolRequest, _ MetadataInput) (*mcp.CallToolResult, MetadataOutput, error) {
		return nil, MetadataOutput{Revision: repository.Revision}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "context_bundle_create", Description: "Create an immutable manifest for selected context files"}, func(_ context.Context, _ *mcp.CallToolRequest, input BundleInput) (*mcp.CallToolResult, BundleOutput, error) {
		bundle, err := repository.CreateBundle(input.Paths)
		return nil, BundleOutput{Bundle: bundle}, err
	})
}

func registerWorkspaceTools(server *mcp.Server, repository contextrepo.Repository, editor *workspaceeditor.Editor, state *workspaceToolState, policy *artifactcontract.WorkspaceMutationPolicy, allowed func(string) bool) {
	if allowed(agentcontract.CapabilityWorkspaceTree) {
		mcp.AddTool(server, &mcp.Tool{Name: agentcontract.CapabilityWorkspaceTree, Description: "List canonical repository-relative workspace file paths; reuse these exact path strings in later tools"}, func(_ context.Context, _ *mcp.CallToolRequest, input TreeInput) (*mcp.CallToolResult, TreeOutput, error) {
			listing, err := editor.TreeListing(input.Path, input.MaxEntries)
			if err == nil {
				state.recordTree(input.Path, listing)
			}
			return nil, treeOutput(input.Path, listing), err
		})
	}
	if allowed(agentcontract.CapabilityWorkspaceRead) {
		mcp.AddTool(server, &mcp.Tool{Name: agentcontract.CapabilityWorkspaceRead, Description: "Read a workspace file before editing it"}, func(_ context.Context, _ *mcp.CallToolRequest, input ReadInput) (*mcp.CallToolResult, workspaceeditor.File, error) {
			file, err := editor.Read(input.Path, input.MaxBytes)
			if err == nil {
				state.recordRead(file.Path)
			} else if errors.Is(err, fs.ErrNotExist) {
				err = state.missingPathError(input.Path, agentcontract.CapabilityWorkspaceRead)
			}
			return nil, file, err
		})
	}
	if allowed(agentcontract.CapabilityWorkspaceSearch) {
		mcp.AddTool(server, &mcp.Tool{Name: agentcontract.CapabilityWorkspaceSearch, Description: "Search text content inside existing workspace files; this does not search filenames, so use workspace_tree to discover paths"}, func(_ context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
			matches, err := editor.Search(input.Path, input.Query, input.MaxResults)
			return nil, SearchOutput{Matches: fromEditorMatches(matches), Revision: repository.Revision}, err
		})
	}
	if allowed(agentcontract.CapabilityWorkspaceWrite) {
		mcp.AddTool(server, &mcp.Tool{
			Name:        agentcontract.CapabilityWorkspaceWrite,
			Description: "Replace an existing UTF-8 file using an exact path returned by workspace_tree and successfully read with workspace_read; path may be omitted only when contract authority identifies one unique inspected target"},
			func(_ context.Context, _ *mcp.CallToolRequest, input UpdateInput) (*mcp.CallToolResult, WriteOutput, error) {
				path, err := resolveWorkspaceWritePath(input.Path, state, policy)
				if err != nil {
					return nil, WriteOutput{}, err
				}
				input.Path = path
				expected, currentPath, exists, err := trustedWorkspaceDigest(editor, input.Path)
				if err != nil {
					return nil, WriteOutput{}, err
				}
				if !exists {
					return nil, WriteOutput{}, state.missingPathError(input.Path, agentcontract.CapabilityWorkspaceWrite)
				}
				if err := state.requireUpdate(currentPath); err != nil {
					return nil, WriteOutput{}, err
				}
				if err := policy.ValidateUpdate(currentPath); err != nil {
					return nil, WriteOutput{}, err
				}
				file, err := editor.Write(input.Path, input.Content, expected)
				if err != nil {
					return nil, WriteOutput{Path: input.Path}, err
				}
				state.recordRead(file.Path)

				return nil, WriteOutput{
					Path:      file.Path,
					Digest:    file.Digest,
					ByteCount: len([]byte(input.Content)),
					Changed:   file.Digest != expected,
				}, nil
			},
		)
	}
	if allowed(agentcontract.CapabilityWorkspaceCreate) {
		mcp.AddTool(server, &mcp.Tool{Name: agentcontract.CapabilityWorkspaceCreate, Description: "Create an absent UTF-8 file at an exact action:add path after workspace_tree; workspace_read is not required for an absent path"}, func(_ context.Context, _ *mcp.CallToolRequest, input WriteInput) (*mcp.CallToolResult, workspaceeditor.File, error) {
			expected, currentPath, exists, err := trustedWorkspaceDigest(editor, input.Path)
			if err != nil {
				return nil, workspaceeditor.File{}, err
			}
			if exists {
				return nil, workspaceeditor.File{}, fmt.Errorf("workspace path %q already exists; workspace_create cannot replace files", currentPath)
			}
			if err := state.requireCreate(currentPath); err != nil {
				return nil, workspaceeditor.File{}, err
			}
			if err := policy.ValidateCreate(currentPath); err != nil {
				return nil, workspaceeditor.File{}, err
			}
			file, err := editor.Write(currentPath, input.Content, expected)
			if err == nil {
				state.recordRead(file.Path)
			}
			return nil, file, err
		})
	}
	if allowed(agentcontract.CapabilityWorkspaceReplace) {
		mcp.AddTool(server, &mcp.Tool{
			Name: agentcontract.CapabilityWorkspaceReplace,
			Description: "Replace exact text in an inspected workspace file. " +
				"oldText must be a nonempty exact snippet that occurs once. " +
				"For insertion, select a unique existing anchor and include that anchor in newText. " +
				"Use workspace_write for a complete-file rewrite."},
			func(_ context.Context, _ *mcp.CallToolRequest, input ReplaceInput) (*mcp.CallToolResult, WriteOutput, error) {
				expected, currentPath, exists, err := trustedWorkspaceDigest(editor, input.Path)
				if err != nil {
					return nil, WriteOutput{}, err
				}
				if !exists {
					return nil, WriteOutput{}, state.missingPathError(input.Path, agentcontract.CapabilityWorkspaceReplace)
				}
				if err := state.requireUpdate(currentPath); err != nil {
					return nil, WriteOutput{}, err
				}
				if err := policy.ValidateUpdate(currentPath); err != nil {
					return nil, WriteOutput{}, err
				}
				file, err := editor.Replace(input.Path, input.OldText, input.NewText, expected, input.ExpectedOccurrences)
				if err != nil {
					return nil, WriteOutput{}, err
				}
				state.recordRead(file.Path)
				return nil, WriteOutput{
					Path:      file.Path,
					Digest:    file.Digest,
					ByteCount: len([]byte(file.Content)),
					Changed:   file.Digest != expected,
				}, err
			})
	}
	if allowed(agentcontract.CapabilityWorkspaceDelete) {
		mcp.AddTool(server, &mcp.Tool{Name: agentcontract.CapabilityWorkspaceDelete, Description: "Delete an inspected workspace file"}, func(_ context.Context, _ *mcp.CallToolRequest, input DeleteInput) (*mcp.CallToolResult, DeleteOutput, error) {
			expected, currentPath, exists, err := trustedWorkspaceDigest(editor, input.Path)
			if err != nil {
				return nil, DeleteOutput{Path: input.Path}, err
			}
			if !exists {
				return nil, DeleteOutput{Path: input.Path}, fmt.Errorf("workspace path %q does not exist", input.Path)
			}
			if err := state.requireUpdate(currentPath); err != nil {
				return nil, DeleteOutput{Path: input.Path}, err
			}
			if err := policy.ValidateDelete(currentPath); err != nil {
				return nil, DeleteOutput{Path: input.Path}, err
			}
			err = editor.Delete(input.Path, expected)
			return nil, DeleteOutput{Path: input.Path, Deleted: err == nil}, err
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "workspace_changes", Description: "Return the trusted complete changed-file manifest derived from the attempt overlay"}, func(_ context.Context, _ *mcp.CallToolRequest, _ ChangesInput) (*mcp.CallToolResult, workspaceeditor.Changes, error) {
		changes, err := editor.Changes()
		return nil, changes, err
	})
}

func resolveWorkspaceWritePath(requested string, state *workspaceToolState, policy *artifactcontract.WorkspaceMutationPolicy) (string, error) {
	if strings.TrimSpace(requested) != "" {
		return requested, nil
	}
	paths := policy.AuthorizedUpdatePaths(state.inspectedPaths())
	switch len(paths) {
	case 1:
		return paths[0], nil
	case 0:
		return "", fmt.Errorf("workspace_write path is required; no previously read path is authorized for update")
	default:
		return "", fmt.Errorf("workspace_write path is required; select one authorized previously read path: %s", strings.Join(paths, ", "))
	}
}

func registerAgentTools(server *mcp.Server, editor *workspaceeditor.Editor, state *workspaceToolState, input agentcontract.Input, allowed func(string) bool) error {
	if len(input.Outputs) != 1 {
		return fmt.Errorf("agent MCP sidecar requires exactly one output obligation")
	}
	output := input.Outputs[0]
	registry, err := artifactcontract.NewMaterializerRegistry()
	if err != nil {
		return err
	}
	materializer, err := registry.Resolve(output.Name, output.Version)
	if err != nil {
		return err
	}
	contract := output.Name + "/" + output.Version
	sources, err := loadMaterializationSources(input.Inputs)
	if err != nil {
		return err
	}
	if !allowed(agentcontract.CapabilityWorkspaceRead) {
		return fmt.Errorf("agent capability %q is required", agentcontract.CapabilityWorkspaceRead)
	}
	if !allowed(agentcontract.CapabilityWorkspaceTree) {
		return fmt.Errorf("agent capability %q is required", agentcontract.CapabilityWorkspaceTree)
	}
	if !allowed(agentcontract.CapabilityAgentComplete) {
		return fmt.Errorf("agent capability %q is required", agentcontract.CapabilityAgentComplete)
	}
	if materializer.Kind == artifactcontract.MaterializationCandidate && !allowed(agentcontract.CapabilityCandidateWrite) {
		return fmt.Errorf("%s requires capability %q", contract, agentcontract.CapabilityCandidateWrite)
	}
	if materializer.Kind == artifactcontract.MaterializationWorkspace && editor == nil {
		return fmt.Errorf("%s requires a workspace editor", contract)
	}
	if materializer.Kind == artifactcontract.MaterializationWorkspace &&
		!allowed(agentcontract.CapabilityWorkspaceWrite) &&
		!allowed(agentcontract.CapabilityWorkspaceCreate) &&
		!allowed(agentcontract.CapabilityWorkspaceReplace) &&
		!allowed(agentcontract.CapabilityWorkspaceDelete) {
		return fmt.Errorf("%s requires a workspace mutation capability", contract)
	}

	if allowed(agentcontract.CapabilityCandidateWrite) {
		if materializer.Kind != artifactcontract.MaterializationCandidate {
			return fmt.Errorf("capability %q is incompatible with %s materialization", agentcontract.CapabilityCandidateWrite, contract)
		}
		schema, err := candidateWriteSchema(materializer.CandidateSchema.JSON)
		if err != nil {
			return err
		}
		mcp.AddTool(server, &mcp.Tool{
			Name: agentcontract.CapabilityCandidateWrite, Description: "Write validated candidate evidence for the declared output obligation",
			InputSchema: schema,
		}, func(_ context.Context, _ *mcp.CallToolRequest, candidate CandidateWriteInput) (*mcp.CallToolResult, artifactcontract.CandidateEvidence, error) {
			paths, observed, complete := state.workspacePathEvidence()
			if !observed {
				return nil, artifactcontract.CandidateEvidence{}, fmt.Errorf("workspace_tree must inspect the repository root before candidate_write")
			}
			if _, err := materializer.Materialize(artifactcontract.MaterializationEvidence{
				Candidate: candidate.Content, Sources: sources,
				WorkspacePaths: paths, WorkspacePathsObserved: observed, WorkspacePathsComplete: complete,
			}); err != nil {
				return nil, artifactcontract.CandidateEvidence{}, err
			}
			expectedDigest, err := trustedCandidateDigest(input.StagingPath, contract)
			if err != nil {
				return nil, artifactcontract.CandidateEvidence{}, err
			}
			evidence, err := artifactcontract.WriteCandidate(input.StagingPath, contract, candidate.Content, expectedDigest)
			return nil, evidence, err
		})
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: agentcontract.CapabilityAgentComplete, Description: "Complete the attempt after all required durable evidence has been written",
	}, func(_ context.Context, _ *mcp.CallToolRequest, completion AgentCompleteInput) (*mcp.CallToolResult, AgentCompleteOutput, error) {
		summary, err := validateCompletionSummary(completion.Summary)
		if err != nil {
			return nil, AgentCompleteOutput{}, err
		}
		var evidenceDigest string
		switch materializer.Kind {
		case artifactcontract.MaterializationCandidate:
			evidence, candidate, err := artifactcontract.ReadCandidate(input.StagingPath, contract)
			if err != nil {
				return nil, AgentCompleteOutput{}, err
			}
			if _, err := materializer.Materialize(artifactcontract.MaterializationEvidence{Candidate: candidate, Sources: sources}); err != nil {
				return nil, AgentCompleteOutput{}, err
			}
			evidenceDigest = evidence.Digest
		case artifactcontract.MaterializationWorkspace:
			changes, err := editor.Changes()
			if err != nil {
				return nil, AgentCompleteOutput{}, err
			}
			if len(changes.Files) == 0 {
				return nil, AgentCompleteOutput{}, fmt.Errorf("workspace contains no changes")
			}
			evidenceDigest = changes.EvidenceDigest
		default:
			return nil, AgentCompleteOutput{}, fmt.Errorf("unsupported materialization kind %q", materializer.Kind)
		}
		completed := AgentCompleteOutput{Summary: summary, EvidenceDigest: evidenceDigest}
		if err := writeCompletion(input.StagingPath, completed); err != nil {
			return nil, AgentCompleteOutput{}, err
		}
		return nil, completed, nil
	})
	return nil
}

func candidateWriteSchema(candidateSchema json.RawMessage) (json.RawMessage, error) {
	var content any
	if err := json.Unmarshal(candidateSchema, &content); err != nil {
		return nil, fmt.Errorf("decode candidate schema: %w", err)
	}
	return json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"content"},
		"properties": map[string]any{
			"content": content,
		},
	})
}

func trustedWorkspaceDigest(editor *workspaceeditor.Editor, path string) (digest, currentPath string, exists bool, err error) {
	current, err := editor.Read(path, 0)
	if err == nil {
		return current.Digest, current.Path, true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return workspaceeditor.AbsentDigest, path, false, nil
	}
	return "", "", false, err
}

func trustedCandidateDigest(stagingPath, contract string) (string, error) {
	evidence, _, err := artifactcontract.ReadCandidate(stagingPath, contract)
	if err == nil {
		return evidence.Digest, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return artifactcontract.CandidateDigestAbsent, nil
	}
	return "", err
}

func treeOutput(root string, listing workspaceeditor.TreeListing) TreeOutput {
	if root == "" {
		root = "."
	}
	return TreeOutput{
		Root: root, Entries: listing.Entries,
		TotalEntries: listing.TotalEntries, Truncated: listing.Truncated,
	}
}

func loadMaterializationSources(inputs []agentcontract.ArtifactInput) ([]artifactcontract.SourceArtifact, error) {
	sources := make([]artifactcontract.SourceArtifact, 0, len(inputs))
	for _, input := range inputs {
		content, err := os.ReadFile(input.Path)
		if err != nil {
			return nil, fmt.Errorf("read materialization source %s: %w", input.Contract, err)
		}
		if digest := artifactcontract.DigestBytes(content); digest != input.Digest {
			return nil, fmt.Errorf("materialization source %s digest mismatch: expected %s, got %s", input.Contract, input.Digest, digest)
		}
		sources = append(sources, artifactcontract.SourceArtifact{Contract: input.Contract, Digest: input.Digest, Content: content})
	}
	return sources, nil
}

func validateCompletionSummary(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len([]byte(value)) > agentcontract.MaxRetryFeedbackMessageBytes || !utf8.ValidString(value) {
		return "", fmt.Errorf("summary must contain 1 through %d UTF-8 bytes", agentcontract.MaxRetryFeedbackMessageBytes)
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return "", fmt.Errorf("summary contains a control character")
		}
	}
	return value, nil
}

func writeCompletion(stagingRoot string, completion AgentCompleteOutput) error {
	content, err := json.Marshal(completion)
	if err != nil {
		return fmt.Errorf("encode completion: %w", err)
	}
	path := filepath.Join(stagingRoot, ".agent-completion.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create completion directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".agent-completion-*.tmp")
	if err != nil {
		return fmt.Errorf("create completion temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set completion permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write completion: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close completion: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish completion: %w", err)
	}
	return nil
}

func fromRepositoryMatches(matches []contextrepo.SearchMatch) []SearchMatch {
	output := make([]SearchMatch, len(matches))
	for index, match := range matches {
		output[index] = SearchMatch{Path: match.Path, Line: match.Line, Text: match.Text}
	}
	return output
}

func fromEditorMatches(matches []workspaceeditor.SearchMatch) []SearchMatch {
	output := make([]SearchMatch, len(matches))
	for index, match := range matches {
		output[index] = SearchMatch{Path: match.Path, Line: match.Line, Text: match.Text}
	}
	return output
}

func requireAttemptIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Sovereign-Attempt") == "" && envBool("SOVEREIGN_MCP_REQUIRE_IDENTITY", true) {
			http.Error(response, "attempt identity is required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
