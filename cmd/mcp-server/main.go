package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/SovereignAI/internal/contextrepo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type TreeInput struct {
	Path       string `json:"path,omitempty"`
	MaxEntries int    `json:"maxEntries,omitempty"`
}
type TreeOutput struct {
	Entries []string `json:"entries"`
}
type ReadInput struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"maxBytes,omitempty"`
}
type ReadOutput struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Revision string `json:"revision"`
}
type SearchInput struct {
	Path       string `json:"path,omitempty"`
	Query      string `json:"query"`
	MaxResults int    `json:"maxResults,omitempty"`
}
type SearchOutput struct {
	Matches  []contextrepo.SearchMatch `json:"matches"`
	Revision string                    `json:"revision"`
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

func main() {
	repository := contextrepo.Repository{Root: env("SOVEREIGN_REPOSITORY_ROOT", "/repository"), BundleDir: env("SOVEREIGN_CONTEXT_BUNDLE_DIR", "/workspace/context"), Revision: env("SOVEREIGN_REPOSITORY_REVISION", "unknown")}
	server := mcp.NewServer(&mcp.Implementation{Name: "sovereign-context", Version: "v1alpha1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "repository_tree", Description: "List bounded repository paths"}, func(_ context.Context, _ *mcp.CallToolRequest, input TreeInput) (*mcp.CallToolResult, TreeOutput, error) {
		entries, err := repository.Tree(input.Path, input.MaxEntries)
		return nil, TreeOutput{Entries: entries}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "repository_read", Description: "Read one bounded repository file"}, func(_ context.Context, _ *mcp.CallToolRequest, input ReadInput) (*mcp.CallToolResult, ReadOutput, error) {
		content, err := repository.Read(input.Path, input.MaxBytes)
		return nil, ReadOutput{Path: input.Path, Content: content, Revision: repository.Revision}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "repository_search", Description: "Search repository text with bounded results"}, func(_ context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		matches, err := repository.Search(input.Path, input.Query, input.MaxResults)
		return nil, SearchOutput{Matches: matches, Revision: repository.Revision}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "repository_metadata", Description: "Return immutable repository metadata"}, func(_ context.Context, _ *mcp.CallToolRequest, _ MetadataInput) (*mcp.CallToolResult, MetadataOutput, error) {
		return nil, MetadataOutput{Revision: repository.Revision}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "context_bundle_create", Description: "Create an immutable manifest for selected context files"}, func(_ context.Context, _ *mcp.CallToolRequest, input BundleInput) (*mcp.CallToolResult, BundleOutput, error) {
		bundle, err := repository.CreateBundle(input.Paths)
		return nil, BundleOutput{Bundle: bundle}, err
	})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, SessionTimeout: 15 * time.Minute})
	mux := http.NewServeMux()
	mux.Handle("/mcp", requireAttemptIdentity(handler))
	mux.HandleFunc("/healthz", func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusOK) })
	serverHTTP := &http.Server{Addr: env("SOVEREIGN_MCP_ADDRESS", ":8080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("Sovereign MCP context server listening on %s", serverHTTP.Addr)
	log.Fatal(serverHTTP.ListenAndServe())
}

func requireAttemptIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		// Istio removes externally supplied identity headers and injects the
		// authenticated workload principal before traffic reaches this service.
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
