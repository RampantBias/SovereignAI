package cli

import (
	"bytes"
	"context"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	"google.golang.org/grpc"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type lineageTestClient struct {
	pb.OrchestratorServiceClient
	request   *pb.GetWorkflowLineageRequest
	badDigest bool
}

func (c *lineageTestClient) GetWorkflowLineage(_ context.Context, req *pb.GetWorkflowLineageRequest, _ ...grpc.CallOption) (*pb.GetWorkflowLineageResponse, error) {
	c.request = req
	return &pb.GetWorkflowLineageResponse{ProjectionJson: `{"schemaVersion":"v1","stories":[]}`}, nil
}
func (c *lineageTestClient) GetContextSnapshot(_ context.Context, req *pb.GetContextSnapshotRequest, _ ...grpc.CallOption) (*pb.GetContextSnapshotResponse, error) {
	data := []byte(`{"private":"prompt content"}`)
	digest := artifactcontract.DigestBytes(data)
	if c.badDigest {
		digest = "wrong"
	}
	return &pb.GetContextSnapshotResponse{Snapshot: data, Receipt: &pb.ContextSnapshotReceipt{Id: req.Id, Digest: digest, EventId: audit.ContextEventID(req.Id)}}, nil
}
func TestLineageCommandSelectsRoot(t *testing.T) {
	client := &lineageTestClient{}
	cmd := NewLineageCmd(client)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"wf", "--event", "retry"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if client.request.WorkflowId != "wf" || client.request.EventId != "retry" || !strings.Contains(out.String(), "stories") {
		t.Fatal("incorrect projection command")
	}
}
func TestPrivateContextCommandRequiresNewFileAndChecksDigest(t *testing.T) {
	client := &lineageTestClient{}
	path := filepath.Join(t.TempDir(), "context.json")
	run := func(args ...string) (string, error) {
		cmd := NewContextSnapshotCmd(client)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	if _, err := run("snapshot"); err == nil {
		t.Fatal("private context allowed without an explicit destination")
	}
	client.badDigest = true
	if _, err := run("snapshot", "--output", path); err == nil {
		t.Fatal("accepted wrong digest")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("wrote unverified context")
	}
	client.badDigest = false
	out, err := run("snapshot", "--output", path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "prompt content") {
		t.Fatal("printed raw context")
	}
	if _, err := run("snapshot", "--output", path); err == nil {
		t.Fatal("overwrote existing file")
	}
}
