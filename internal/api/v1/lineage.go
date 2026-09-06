package v1

import (
	"context"
	"encoding/json"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/audit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

func (s *Server) GetWorkflowLineage(ctx context.Context, req *pb.GetWorkflowLineageRequest) (*pb.GetWorkflowLineageResponse, error) {
	if _, err := requireWorkflowRequester(ctx); err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.WorkflowId) == "" {
		return nil, status.Error(codes.InvalidArgument, "workflow id is required")
	}
	if s.Auditor == nil {
		return nil, status.Error(codes.FailedPrecondition, "audit recorder is not configured")
	}
	events, err := s.Auditor.ListWorkflow(ctx, req.WorkflowId)
	if err != nil {
		return nil, status.Error(codes.Internal, "could not read workflow evidence")
	}
	projection, err := audit.BuildProjection(req.WorkflowId, req.EventId, events)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	data, err := json.Marshal(projection)
	if err != nil {
		return nil, status.Error(codes.Internal, "could not encode projection")
	}
	return &pb.GetWorkflowLineageResponse{ProjectionJson: string(data)}, nil
}
