package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/inference"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type snapshotUploadStub struct {
	pb.UnimplementedOrchestratorServiceServer
	store func(context.Context, *pb.StoreContextSnapshotRequest) (*pb.ContextSnapshotReceipt, error)
}

func (s *snapshotUploadStub) StoreContextSnapshot(ctx context.Context, r *pb.StoreContextSnapshotRequest) (*pb.ContextSnapshotReceipt, error) {
	return s.store(ctx, r)
}
func snapshotTestEndpoint(t *testing.T, store func(context.Context, *pb.StoreContextSnapshotRequest) (*pb.ContextSnapshotReceipt, error)) *agentcontract.ContextCapture {
	t.Helper()
	rpc := grpc.NewServer()
	pb.RegisterOrchestratorServiceServer(rpc, &snapshotUploadStub{store: store})
	server := httptest.NewUnstartedServer(rpc)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Cleanup(rpc.Stop)
	root := t.TempDir()
	ca := filepath.Join(root, "ca.crt")
	token := filepath.Join(root, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("private-upload-token"), 0600); err != nil {
		t.Fatal(err)
	}
	return &agentcontract.ContextCapture{Endpoint: strings.TrimPrefix(server.URL, "https://"), Namespace: "wf", AgentRun: "author", AgentRunUID: "author-uid", CAPath: ca, CredentialPath: token}
}
func snapshotTestReceipt(req *pb.StoreContextSnapshotRequest) *pb.ContextSnapshotReceipt {
	id := audit.DeterministicID("initial-context/v1", req.AgentRunUid)
	return &pb.ContextSnapshotReceipt{Id: id, Digest: artifactcontract.DigestBytes(req.Snapshot), EventId: audit.ContextEventID(id)}
}
func TestInitialContextUploadRetriesSameBytesAndChecksReceipt(t *testing.T) {
	for _, scenario := range []string{"retry", "reject", "wrong-receipt"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			var first string
			capture := snapshotTestEndpoint(t, func(ctx context.Context, req *pb.StoreContextSnapshotRequest) (*pb.ContextSnapshotReceipt, error) {
				n := calls.Add(1)
				if metadata.ValueFromIncomingContext(ctx, "x-sovereign-context-token")[0] != "private-upload-token" {
					t.Error("missing token")
				}
				if strings.Contains(string(req.Snapshot), "private-upload-token") || strings.Contains(string(req.Snapshot), "credentialPath") {
					t.Error("credential leaked into context")
				}
				if n == 1 {
					first = string(req.Snapshot)
				} else if first != string(req.Snapshot) {
					t.Error("retry changed snapshot")
				}
				if scenario == "reject" {
					return nil, status.Error(codes.Unauthenticated, "rejected")
				}
				if scenario == "retry" && n == 1 {
					return nil, status.Error(codes.Unavailable, "temporary")
				}
				receipt := snapshotTestReceipt(req)
				if scenario == "wrong-receipt" {
					receipt.Digest = "wrong"
				}
				return receipt, nil
			})
			input := agentcontract.Input{ContextCapture: capture, RetryFeedback: &agentcontract.RetryFeedback{PreviousAttemptRef: "failed-test", Code: "TestRunCodeError", Message: "stale test"}}
			request := inference.ChatRequest{Model: "demo", Messages: []inference.Message{{Role: "system", Content: "private instructions"}}, Tools: []inference.ToolDefinition{{Type: "function", Function: inference.ToolFunctionDefinition{Name: "workspace_tree", Parameters: json.RawMessage(`{}`)}}}}
			err := saveInitialContext(context.Background(), input, nil, request)
			if (err == nil) != (scenario == "retry") {
				t.Fatalf("upload result: %v", err)
			}
			want := int32(1)
			if scenario == "retry" {
				want = 2
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d", calls.Load())
			}
			var saved audit.InitialContext
			if err := json.Unmarshal([]byte(first), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.RetryFeedback.Message != "stale test" || saved.Request.Messages[0].Content != "private instructions" || len(saved.Request.Tools) != 1 {
				t.Fatal("lost initial context")
			}
		})
	}
}

func TestSnapshotFailurePreventsInference(t *testing.T) {
	var inferenceCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { inferenceCalls.Add(1) }))
	defer model.Close()
	client, err := inference.NewClient(model.URL, time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	mcp := startMockMCPServer(t, root, "implementation-plan/v1", make(chan mockWorkspaceTreeInput, 1))
	capture := snapshotTestEndpoint(t, func(context.Context, *pb.StoreContextSnapshotRequest) (*pb.ContextSnapshotReceipt, error) {
		return nil, status.Error(codes.Unauthenticated, "rejected")
	})
	input := agentcontract.Input{ContextCapture: capture, MCPServer: mcp.URL + "/mcp", Outputs: []agentcontract.OutputObligation{{Name: "implementation-plan", Version: "v1"}}, Capabilities: []string{agentcontract.CapabilityWorkspaceRead, agentcontract.CapabilityWorkspaceTree, agentcontract.CapabilityCandidateWrite, agentcontract.CapabilityAgentComplete}}
	if _, err := generateWithMCP(context.Background(), input, client, "system", "task", nil); err == nil {
		t.Fatal("expected capture failure")
	}
	if inferenceCalls.Load() != 0 {
		t.Fatal("model was called before capture was saved")
	}
}
