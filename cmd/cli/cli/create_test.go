package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SovereignAI/internal/api/v1/pb"
	"google.golang.org/grpc"
)

type recordingOrchestratorClient struct {
	pb.OrchestratorServiceClient
	request *pb.CreateWorkflowRequest
}

func (c *recordingOrchestratorClient) CreateWorkflow(_ context.Context, request *pb.CreateWorkflowRequest, _ ...grpc.CallOption) (*pb.CreateWorkflowResponse, error) {
	c.request = request
	return &pb.CreateWorkflowResponse{WorkflowId: "workflow-1"}, nil
}

func TestCreateWorkflowSendsExactChangeRequestBytes(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "workflow.yaml")
	changeRequestPath := filepath.Join(root, "change-request.json")
	manifestBytes := []byte("apiVersion: aim.sovereign.io/v1alpha1\nkind: SovereignWorkflow\n")
	changeRequestBytes := []byte("{\n  \"summary\": \"preserve spacing\"\n}\n")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changeRequestPath, changeRequestBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	client := &recordingOrchestratorClient{}
	command := NewCreateCmd(client)
	command.SetArgs([]string{
		"workflow", "--project", "platform", "--file", manifestPath, "--change-request", changeRequestPath,
	})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.request == nil {
		t.Fatal("CreateWorkflow was not called")
	}
	if !bytes.Equal(client.request.GetChangeRequestContent(), changeRequestBytes) {
		t.Fatalf("change-request bytes changed in transit: got %q, want %q", client.request.GetChangeRequestContent(), changeRequestBytes)
	}
	if client.request.GetManifestContent() != string(manifestBytes) {
		t.Fatalf("manifest content changed in transit: got %q, want %q", client.request.GetManifestContent(), manifestBytes)
	}
}
