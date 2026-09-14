package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/SovereignAI/internal/api/requestidentity"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllers"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type submissionAuditFailure struct {
	*audit.MemoryRecorder
	failed bool
}

func (r *submissionAuditFailure) Append(ctx context.Context, event audit.Event) error {
	if event.Type == "ApprovalDecisionSubmitted" && !r.failed {
		r.failed = true
		return errors.New("audit unavailable")
	}
	return r.MemoryRecorder.Append(ctx, event)
}
func TestPersistedSubmissionRecoversAuditWithoutDuplicateDecision(t *testing.T) {
	for _, recovery := range []string{"API retry", "controller retry"} {
		t.Run(recovery, func(t *testing.T) {
			workflow, attempt, request := approvalFixture()
			kube := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(&v1alpha1.ApprovalRequest{}).
				WithObjects(workflow, attempt, request).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				obj.SetUID(types.UID("persisted-" + obj.GetName()))
				return c.Create(ctx, obj, opts...)
			}}).Build()
			recorder := &submissionAuditFailure{MemoryRecorder: audit.NewMemoryRecorder()}
			server := NewServer(kube, recorder)
			ctx := requestidentity.WithIdentity(context.Background(), requestidentity.Identity{Subject: "alice", Groups: []string{"maintainers"}})
			submission := &pb.ApprovalSubmission{Action: "approve", WorkflowId: "wf", RequestUid: string(request.UID)}
			if _, err := server.SubmitApproval(ctx, submission); err == nil {
				t.Fatal("expected audit failure after decision creation")
			}
			if recovery == "API retry" {
				for range 2 {
					if _, err := server.SubmitApproval(ctx, submission); err != nil {
						t.Fatal(err)
					}
				}
			}
			reconciler := &controllers.ApprovalRequestReconciler{Client: kube, Audit: recorder}
			for range 2 {
				if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(request)}); err != nil {
					t.Fatal(err)
				}
			}
			var decisions v1alpha1.ApprovalDecisionList
			if err := kube.List(ctx, &decisions); err != nil {
				t.Fatal(err)
			}
			if len(decisions.Items) != 1 {
				t.Fatalf("created %d decisions", len(decisions.Items))
			}
			if err := kube.Get(ctx, client.ObjectKeyFromObject(request), request); err != nil {
				t.Fatal(err)
			}
			if request.Status.Phase != v1alpha1.PhaseSucceeded || request.Status.DecisionUID != decisions.Items[0].UID {
				t.Fatal("persisted decision was not admitted")
			}
			if len(recorder.AllEvents()) != 2 || !recorder.Has("ApprovalDecisionSubmitted") || !recorder.Has("ApprovalDecisionAdmitted") {
				t.Fatal("missing or duplicated approval events")
			}
		})
	}
}
