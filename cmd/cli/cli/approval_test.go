package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type approvalClient struct {
	pb.OrchestratorServiceClient
	request     *pb.ApprovalSubmission
	response    *pb.ApprovalResponse
	err         error
	hasDeadline bool
}

func (c *approvalClient) SubmitApproval(ctx context.Context, req *pb.ApprovalSubmission, _ ...grpc.CallOption) (*pb.ApprovalResponse, error) {
	c.request = req
	_, c.hasDeadline = ctx.Deadline()
	return c.response, c.err
}

func TestApprovalCommandSubmission(t *testing.T) {
	for _, action := range []string{"approve", "deny", "show"} {
		t.Run(action, func(t *testing.T) {
			api := &approvalClient{response: &pb.ApprovalResponse{Success: true, Message: "request handled"}}
			cmd := NewRootCmd(api)
			args := []string{"approval", action, "wf"}
			if action != "show" {
				args = append(args, "--request-uid", "request-uid")
			}
			out := &bytes.Buffer{}
			cmd.SetOut(out)
			cmd.SetArgs(args)
			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if api.request == nil || api.request.GetAction() != action || api.request.GetWorkflowId() != "wf" || !api.hasDeadline {
				t.Fatalf("incorrect request: %#v", api.request)
			}
			if action != "show" && api.request.GetRequestUid() != "request-uid" {
				t.Fatal("request UID was not sent")
			}
			if out.String() != "request handled\n" {
				t.Fatalf("output = %q", out.String())
			}
		})
	}
}

func TestApprovalCommandRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"approve", "wf"}, {"deny", "wf"}, {"approve", "wf", "--request-uid", " "},
		{"invalid", "wf"}, {"approve"}, {"approve", "wf", "extra", "-r", "uid"}, {"show", " "},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			api := &approvalClient{}
			cmd := NewApprovalCmd(api)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
			if api.request != nil {
				t.Fatal("invalid arguments reached the API")
			}
		})
	}
}

func TestApprovalCommandDoesNotReportFailedSubmissionAsSuccess(t *testing.T) {
	for _, api := range []*approvalClient{
		{err: status.Error(codes.PermissionDenied, "wrong group")},
		{response: &pb.ApprovalResponse{Success: false, Message: "rejected"}},
	} {
		cmd := NewApprovalCmd(api)
		out := &bytes.Buffer{}
		cmd.SetOut(out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs([]string{"approve", "wf", "-r", "uid"})
		if err := cmd.Execute(); err == nil {
			t.Fatal("failure was not returned")
		}
		if out.Len() != 0 {
			t.Fatalf("failure printed success output: %q", out.String())
		}
	}
}
