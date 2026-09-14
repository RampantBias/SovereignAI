package v1

import (
	"context"
	"encoding/json"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
)

func (s *Server) GetWorkflowLineage(ctx context.Context, req *pb.GetWorkflowLineageRequest) (*pb.GetWorkflowLineageResponse, error) {
	if _, err := requireWorkflowRequester(ctx); err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.WorkflowId) == "" {
		return nil, status.Error(codes.InvalidArgument, "workflow id is required")
	}
	if req.View != "" && req.View != "stories" && req.View != "workflow" {
		return nil, status.Error(codes.InvalidArgument, "view must be stories or workflow")
	}
	if req.View == "workflow" && req.EventId != "" {
		return nil, status.Error(codes.InvalidArgument, "event selection is only supported for the stories view")
	}
	if s.Auditor == nil {
		return nil, status.Error(codes.FailedPrecondition, "audit recorder is not configured")
	}
	events, err := s.Auditor.ListWorkflow(ctx, req.WorkflowId)
	if err != nil {
		return nil, status.Error(codes.Internal, "could not read workflow evidence")
	}
	var projection any
	if req.View == "workflow" {
		projection, err = s.workflowLineage(ctx, req.WorkflowId, events)
	} else {
		projection, err = audit.BuildProjection(req.WorkflowId, req.EventId, events)
	}
	if err != nil {
		if status.Code(err) != codes.Unknown {
			return nil, err
		}
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	data, err := json.Marshal(projection)
	if err != nil {
		return nil, status.Error(codes.Internal, "could not encode projection")
	}
	return &pb.GetWorkflowLineageResponse{ProjectionJson: string(data)}, nil
}

func (s *Server) workflowLineage(ctx context.Context, id string, events []audit.Event) (audit.WorkflowView, error) {
	if s.Client == nil {
		return audit.WorkflowView{}, status.Error(codes.FailedPrecondition, "workflow navigation requires the Kubernetes client")
	}
	var workflows v1alpha1.SovereignWorkflowList
	if err := s.Client.List(ctx, &workflows, client.MatchingLabels{"sovereign-ai.io/workflow-id": id}); err != nil {
		return audit.WorkflowView{}, status.Error(codes.Internal, "could not read workflow navigation")
	}
	if len(workflows.Items) != 1 {
		return audit.WorkflowView{}, status.Error(codes.FailedPrecondition, "workflow view requires one retained workflow; use --view stories for audit-only evidence after cleanup")
	}
	wf := &workflows.Items[0]
	var attempts v1alpha1.StepAttemptList
	var approvals v1alpha1.ApprovalRequestList
	if err := s.Client.List(ctx, &attempts, client.InNamespace(wf.Namespace)); err != nil {
		return audit.WorkflowView{}, status.Error(codes.Internal, "could not read workflow attempts")
	}
	if err := s.Client.List(ctx, &approvals, client.InNamespace(wf.Namespace)); err != nil {
		return audit.WorkflowView{}, status.Error(codes.Internal, "could not read approval requests")
	}
	return audit.BuildWorkflowView(wf, attempts.Items, approvals.Items, events)
}
