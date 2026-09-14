package cli

import (
	"bytes"
	"context"
	"errors"
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
	request    *pb.GetWorkflowLineageRequest
	badDigest  bool
	projection string
}

func (c *lineageTestClient) GetWorkflowLineage(_ context.Context, req *pb.GetWorkflowLineageRequest, _ ...grpc.CallOption) (*pb.GetWorkflowLineageResponse, error) {
	c.request = req
	if c.projection != "" {
		return &pb.GetWorkflowLineageResponse{ProjectionJson: c.projection}, nil
	}
	return &pb.GetWorkflowLineageResponse{ProjectionJson: `{"schemaVersion":"v1","stories":[]}`}, nil
}

func TestLineageHTMLExportPreservesLastGoodSnapshot(t *testing.T) {
	client := &lineageTestClient{projection: `{"schemaVersion":"v1","view":"workflow","workflow":{"name":"wf","uid":"wf-uid"},"attempts":[]}`}
	path := filepath.Join(t.TempDir(), "lineage.html")
	run := func(args ...string) error {
		cmd := NewLineageCmd(client)
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		return cmd.Execute()
	}
	if err := run("wf", "--format", "html", "--output", path); err != nil {
		t.Fatal(err)
	}
	if client.request.View != "workflow" {
		t.Fatal("HTML did not request workflow view")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before), "<!doctype html>") {
		t.Fatal("not HTML")
	}
	client.projection = `{"schemaVersion":"v1","stories":[]}`
	if err := run("wf", "--format", "html", "--output", path); err == nil {
		t.Fatal("accepted older server response")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed export destroyed last good snapshot")
	}
	client.projection = `{"schemaVersion":"v1","view":"workflow","workflow":{"name":"updated","uid":"wf-uid"}}`
	if err := run("wf", "--format", "html", "--output", path); err != nil {
		t.Fatal("could not refresh export", err)
	}
	after, _ = os.ReadFile(path)
	if bytes.Equal(before, after) {
		t.Fatal("refresh did not replace export")
	}
	for _, args := range [][]string{{"wf", "--format", "html"}, {"wf", "--view", "workflow", "--event", "retry"}, {"wf", "--view", "unknown"}, {"wf", "--format", "bad"}} {
		if err := run(args...); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
}

func TestLineageHTMLStdoutForHostRedirection(t *testing.T) {
	client := &lineageTestClient{projection: `{"schemaVersion":"v1","view":"workflow","workflow":{"name":"wf","uid":"wf-uid"}}`}
	cmd := NewLineageCmd(client)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"wf", "--format", "html", "--output", "-"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "<!doctype html>") || strings.Contains(out.String(), "Saved lineage to") || client.request.View != "workflow" {
		t.Fatal("stdout must contain only the rendered HTML")
	}
	missing := filepath.Join(t.TempDir(), "missing", "lineage.html")
	err := writeLineageExport(missing, []byte("html"))
	if err == nil || !strings.Contains(err.Error(), "CLI filesystem") || !strings.Contains(err.Error(), "--output -") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing destination error is not actionable: %v", err)
	}
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
