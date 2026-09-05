package v1

import (
	"context"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/requestidentity"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func approvalFixture() (*v1alpha1.SovereignWorkflow, *v1alpha1.StepAttempt, *v1alpha1.ApprovalRequest) {
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: "wf-ns", UID: "wf-uid", Labels: map[string]string{"sovereign-ai.io/workflow-id": "wf"}},
		Spec:       v1alpha1.SovereignWorkflowSpec{WorkflowID: "wf"},
	}
	workflow.Status.Phase = "Running" // Workflow visibility has not been implemented yet.
	workflow.Status.ActiveAttemptRef = "gate-1"
	workflow.Status.ActiveStepName = "product-approval"
	attempt := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: "gate-1", Namespace: workflow.Namespace, UID: "attempt-uid",
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(workflow, v1alpha1.GroupVersion.WithKind("SovereignWorkflow"))}},
		Spec:   v1alpha1.StepAttemptSpec{WorkflowRef: v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}, StepName: "product-approval", RetryNumber: 1, Kind: v1alpha1.ExecutionKindHumanGate},
		Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhaseAwaitingApproval, ExecutionRef: &v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ApprovalRequest", Name: "gate-1"}},
	}
	approval := &v1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "gate-1", Namespace: workflow.Namespace, UID: "request-uid",
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(attempt, v1alpha1.GroupVersion.WithKind("StepAttempt"))}},
		Spec: v1alpha1.ApprovalRequestSpec{WorkflowRef: attempt.Spec.WorkflowRef, AttemptRef: v1alpha1.UIDReference{Name: attempt.Name, UID: attempt.UID}, StepName: attempt.Spec.StepName, Attempt: 1,
			Approval: v1alpha1.ApprovalSpec{Mode: v1alpha1.AnyOf, RequiredGroups: []string{"maintainers"}, DenyBehavior: "Fail"}},
		Status: v1alpha1.ApprovalRequestStatus{Phase: v1alpha1.PhaseAwaitingApproval},
	}
	return workflow, attempt, approval
}

func TestSubmitApprovalPersistsHumanIntentOnly(t *testing.T) {
	for _, action := range []string{"approve", "deny"} {
		t.Run(action, func(t *testing.T) {
			workflow, attempt, approval := approvalFixture()
			kube := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(workflow, attempt, approval).Build()
			server := NewServer(kube, nil)
			ctx := requestidentity.WithIdentity(context.Background(), requestidentity.Identity{Subject: "alice", Groups: []string{"maintainers"}})
			req := &pb.ApprovalSubmission{Action: action, WorkflowId: "wf", RequestUid: string(approval.UID)}
			for range 2 {
				response, err := server.SubmitApproval(ctx, req)
				if err != nil || !response.GetSuccess() {
					t.Fatalf("SubmitApproval = %v, %v", response, err)
				}
			}
			var decisions v1alpha1.ApprovalDecisionList
			if err := kube.List(ctx, &decisions); err != nil {
				t.Fatal(err)
			}
			if len(decisions.Items) != 1 {
				t.Fatalf("got %d decisions", len(decisions.Items))
			}
			decision := &decisions.Items[0]
			want := v1alpha1.Approved
			if action == "deny" {
				want = v1alpha1.Denied
			}
			if decision.Spec.Decision != want || decision.Spec.Subject.SubjectId != "alice" || len(decision.Spec.Subject.Groups) != 1 || decision.Spec.Subject.Groups[0] != "maintainers" || decision.Spec.AuthoredAt == nil {
				t.Fatalf("incorrect authored decision: %#v", decision.Spec)
			}
			if decision.Spec.WorkflowRef != approval.Spec.WorkflowRef || decision.Spec.StepAttemptRef != approval.Spec.AttemptRef || decision.Spec.ApprovalRequestRef.UID != approval.UID || !metav1.IsControlledBy(decision, approval) {
				t.Fatalf("incorrect decision binding: %#v", decision)
			}
			for _, object := range []client.Object{workflow, attempt, approval} {
				if err := kube.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
					t.Fatal(err)
				}
			}
			if workflow.Status.Phase != "Running" || attempt.Status.Phase != v1alpha1.PhaseAwaitingApproval || approval.Status.Phase != v1alpha1.PhaseAwaitingApproval || approval.Status.DecisionRef != "" {
				t.Fatal("submission changed controller-owned status")
			}
			req.Action = "deny"
			if action == "deny" {
				req.Action = "approve"
			}
			if _, err := server.SubmitApproval(ctx, req); status.Code(err) != codes.AlreadyExists {
				t.Fatalf("conflicting decision: %v", err)
			}
		})
	}
}

func TestSubmitApprovalRejectsInvalidAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*v1alpha1.SovereignWorkflow, *v1alpha1.StepAttempt, *v1alpha1.ApprovalRequest, *pb.ApprovalSubmission, *requestidentity.Identity)
		code   codes.Code
	}{
		{"unauthenticated", func(_ *v1alpha1.SovereignWorkflow, _ *v1alpha1.StepAttempt, _ *v1alpha1.ApprovalRequest, _ *pb.ApprovalSubmission, i *requestidentity.Identity) {
			i.Subject = ""
		}, codes.Unauthenticated},
		{"wrong group", func(_ *v1alpha1.SovereignWorkflow, _ *v1alpha1.StepAttempt, _ *v1alpha1.ApprovalRequest, _ *pb.ApprovalSubmission, i *requestidentity.Identity) {
			i.Groups = []string{"submitters"}
		}, codes.PermissionDenied},
		{"stale request", func(_ *v1alpha1.SovereignWorkflow, _ *v1alpha1.StepAttempt, _ *v1alpha1.ApprovalRequest, r *pb.ApprovalSubmission, _ *requestidentity.Identity) {
			r.RequestUid = "old-uid"
		}, codes.FailedPrecondition},
		{"missing UID", func(_ *v1alpha1.SovereignWorkflow, _ *v1alpha1.StepAttempt, _ *v1alpha1.ApprovalRequest, r *pb.ApprovalSubmission, _ *requestidentity.Identity) {
			r.RequestUid = ""
		}, codes.InvalidArgument},
		{"unknown action", func(_ *v1alpha1.SovereignWorkflow, _ *v1alpha1.StepAttempt, _ *v1alpha1.ApprovalRequest, r *pb.ApprovalSubmission, _ *requestidentity.Identity) {
			r.Action = "yes"
		}, codes.InvalidArgument},
		{"finished workflow", func(w *v1alpha1.SovereignWorkflow, _ *v1alpha1.StepAttempt, _ *v1alpha1.ApprovalRequest, _ *pb.ApprovalSubmission, _ *requestidentity.Identity) {
			w.Status.Phase = "Succeeded"
		}, codes.FailedPrecondition},
		{"finished attempt", func(_ *v1alpha1.SovereignWorkflow, a *v1alpha1.StepAttempt, _ *v1alpha1.ApprovalRequest, _ *pb.ApprovalSubmission, _ *requestidentity.Identity) {
			a.Status.Phase = v1alpha1.PhaseSucceeded
		}, codes.FailedPrecondition},
		{"wrong attempt UID", func(_ *v1alpha1.SovereignWorkflow, _ *v1alpha1.StepAttempt, a *v1alpha1.ApprovalRequest, _ *pb.ApprovalSubmission, _ *requestidentity.Identity) {
			a.Spec.AttemptRef.UID = "old-attempt"
		}, codes.FailedPrecondition},
		{"wrong workflow UID", func(_ *v1alpha1.SovereignWorkflow, _ *v1alpha1.StepAttempt, a *v1alpha1.ApprovalRequest, _ *pb.ApprovalSubmission, _ *requestidentity.Identity) {
			a.Spec.WorkflowRef.UID = "old-workflow"
		}, codes.FailedPrecondition},
		{"not awaiting", func(_ *v1alpha1.SovereignWorkflow, _ *v1alpha1.StepAttempt, a *v1alpha1.ApprovalRequest, _ *pb.ApprovalSubmission, _ *requestidentity.Identity) {
			a.Status.Phase = v1alpha1.PhasePending
		}, codes.FailedPrecondition},
		{"unsupported mode", func(_ *v1alpha1.SovereignWorkflow, _ *v1alpha1.StepAttempt, a *v1alpha1.ApprovalRequest, _ *pb.ApprovalSubmission, _ *requestidentity.Identity) {
			a.Spec.Approval.Mode = "AllOf"
		}, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflow, attempt, approval := approvalFixture()
			req := &pb.ApprovalSubmission{Action: "approve", WorkflowId: "wf", RequestUid: string(approval.UID)}
			identity := requestidentity.Identity{Subject: "alice", Groups: []string{"maintainers"}}
			tc.change(workflow, attempt, approval, req, &identity)
			kube := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(workflow, attempt, approval).Build()
			ctx := requestidentity.WithIdentity(context.Background(), identity)
			if _, err := NewServer(kube, nil).SubmitApproval(ctx, req); status.Code(err) != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
			var decisions v1alpha1.ApprovalDecisionList
			if err := kube.List(ctx, &decisions); err != nil {
				t.Fatal(err)
			}
			if len(decisions.Items) != 0 {
				t.Fatal("rejected submission created a decision")
			}
		})
	}
}

func TestShowApprovalIsReadOnly(t *testing.T) {
	workflow, attempt, approval := approvalFixture()
	kube := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(workflow, attempt, approval).Build()
	ctx := requestidentity.WithIdentity(context.Background(), requestidentity.Identity{Subject: "alice", Groups: []string{"maintainers"}})
	response, err := NewServer(kube, nil).SubmitApproval(ctx, &pb.ApprovalSubmission{Action: "show", WorkflowId: "wf"})
	if err != nil || !response.GetSuccess() || !strings.Contains(response.GetMessage(), "Request UID: request-uid") {
		t.Fatalf("show = %v, %v", response, err)
	}
	var decisions v1alpha1.ApprovalDecisionList
	if err := kube.List(ctx, &decisions); err != nil {
		t.Fatal(err)
	}
	if len(decisions.Items) != 0 {
		t.Fatal("show created a decision")
	}
}
