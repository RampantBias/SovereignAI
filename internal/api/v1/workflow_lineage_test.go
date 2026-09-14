package v1

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SovereignAI/internal/api/requestidentity"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestWorkflowLineageUsesRetainedResourcesAndExistingAuthorization(t *testing.T) {
	s, _, _, _ := snapshotAPIFixture(t)
	ctx := requestidentity.WithIdentity(context.Background(), requestidentity.Identity{Subject: "reviewer", Groups: []string{workflowSubmitterGroup}})
	var wf v1alpha1.SovereignWorkflow
	key := client.ObjectKey{Namespace: "wf", Name: "wf"}
	if err := s.Client.Get(ctx, key, &wf); err != nil {
		t.Fatal(err)
	}
	wf.Labels = map[string]string{"sovereign-ai.io/workflow-id": "wf"}
	if err := s.Client.Update(ctx, &wf); err != nil {
		t.Fatal(err)
	}
	req := &pb.GetWorkflowLineageRequest{WorkflowId: "wf", View: "workflow"}
	response, err := s.GetWorkflowLineage(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var v audit.WorkflowView
	if err := json.Unmarshal([]byte(response.ProjectionJson), &v); err != nil {
		t.Fatal(err)
	}
	if v.View != "workflow" || len(v.Attempts) != 1 || v.Attempts[0].Resource.UID != "attempt-uid" {
		t.Fatal("missing workflow navigation")
	}
	if _, err := s.GetWorkflowLineage(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Fatal("anonymous workflow view allowed")
	}
	req.EventId = "event"
	if _, err := s.GetWorkflowLineage(ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatal("ambiguous view selection accepted")
	}
	req.EventId = ""
	if err := s.Client.Delete(ctx, &wf); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWorkflowLineage(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("cleaned workflow silently appears empty")
	}
	req.View = "stories"
	if _, err := s.GetWorkflowLineage(ctx, req); err != nil {
		t.Fatal("audit-only mode stopped working after cleanup", err)
	}
}
