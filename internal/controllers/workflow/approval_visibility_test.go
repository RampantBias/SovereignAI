package workflow

import (
	"context"
	"encoding/json"
	"github.com/SovereignAI/internal/audit"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/requestidentity"
	v1 "github.com/SovereignAI/internal/api/v1"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/controllers/stepattempt"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWorkflowShowsApprovalWaitUntilActiveAttemptCompletes(t *testing.T) {
	for _, outcome := range []v1alpha1.ResourcePhase{v1alpha1.PhaseSucceeded, v1alpha1.PhaseFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{v1alpha1.AddToScheme, corev1.AddToScheme, batchv1.AddToScheme} {
				if err := add(scheme); err != nil {
					t.Fatal(err)
				}
			}
			workflow := changeRequestWorkflow()
			candidate := changeRequestArtifact(workflow).DeepCopy()
			candidate.Name, candidate.UID = "reviewed-candidate", "candidate-uid"
			candidate.Spec.Contract = v1alpha1.ContractReference{Name: "candidate-revision", Version: "v1"}
			candidate.Spec.Digest = "sha256:" + strings.Repeat("a", 64)
			candidate.Spec.ProducerRef = v1alpha1.TypedLocalReference{Kind: "ImportedSnapshot", Name: "fixture"}
			candidate.Spec.Claims = &v1alpha1.ArtifactClaims{CandidateRevision: &v1alpha1.CandidateRevisionClaims{
				RepositoryURL: "https://github.com/example/calculator.git", Branch: "demo", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}}
			pin := v1alpha1.ArtifactReference{Name: "candidate-revision", Digest: candidate.Spec.Digest, ArtifactRef: &v1alpha1.UIDReference{Name: candidate.Name, UID: candidate.UID}}
			recorder := audit.NewMemoryRecorder()
			workflow.Generation = 1
			workflow.Labels = map[string]string{controllermeta.LabelWorkflow: workflow.Spec.WorkflowID}
			workflow.Spec.Steps = []v1alpha1.StepConfig{
				{Name: "product-approval", Kind: v1alpha1.ExecutionKindHumanGate, Order: 1, MaxAttempts: 1, Inputs: []v1alpha1.ArtifactReference{pin},
					Approval: &v1alpha1.ApprovalSpec{Mode: v1alpha1.AnyOf, RequiredGroups: []string{"maintainers"}, DenyBehavior: "Fail"}},
				{Name: "request-merge", Kind: v1alpha1.ExecutionKindUtility, Order: 2, Inputs: []v1alpha1.ArtifactReference{pin}, RequiresApproval: &v1alpha1.ApprovalRequirement{Step: "product-approval", Subject: "candidate-revision"},
					Utility: &v1alpha1.UtilityOperationRequest{Name: "git.mergeRequest"}},
			}
			workflow.Status.Phase = string(v1alpha1.PhaseRunning)
			workflow.Status.WorkspaceWriterLeaseRef = "writer"
			workflow.Status.ActiveStepName = "product-approval"
			workflow.Status.ActiveAttemptRef = "product-approval-w000-r001"
			attempt := &v1alpha1.StepAttempt{
				ObjectMeta: metav1.ObjectMeta{Name: workflow.Status.ActiveAttemptRef, Namespace: workflow.Namespace, UID: "gate-attempt-uid",
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(workflow, v1alpha1.GroupVersion.WithKind("SovereignWorkflow"))}},
				Spec: v1alpha1.StepAttemptSpec{WorkflowRef: v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID},
					Kind: v1alpha1.ExecutionKindHumanGate, StepName: "product-approval", RetryNumber: 1},
				Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhasePending, ExecutionRef: &v1alpha1.TypedLocalReference{
					APIVersion: v1alpha1.GroupVersion.String(),
					Kind:       "ApprovalRequest",
					Name:       "product-approval-w000-r001"}},
			}
			approval := &v1alpha1.ApprovalRequest{
				ObjectMeta: metav1.ObjectMeta{Name: attempt.Name, Namespace: workflow.Namespace, UID: "approval-request-uid",
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(attempt, v1alpha1.GroupVersion.WithKind("StepAttempt"))}},
				Spec: v1alpha1.ApprovalRequestSpec{
					AttemptRef:  v1alpha1.UIDReference{Name: attempt.Name, UID: attempt.UID},
					WorkflowRef: attempt.Spec.WorkflowRef, StepName: attempt.Spec.StepName, Attempt: attempt.Spec.RetryNumber,
					Approval: *workflow.Spec.Steps[0].Approval, Inputs: []v1alpha1.ArtifactReference{pin},
				},
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.StepAttempt{}, &v1alpha1.ApprovalRequest{}, &v1alpha1.UtilityOperation{}).
				WithObjects(workflow, changeRequestArtifact(workflow), candidate, attempt, approval).
				WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
					if object.GetUID() == "" {
						object.SetUID(types.UID("uid-" + object.GetName()))
					}
					return c.Create(ctx, object, opts...)
				}}).Build()
			approvalReconciler := &controllers.ApprovalRequestReconciler{Client: kube, Audit: recorder}
			if _, err := approvalReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(approval)}); err != nil {
				t.Fatal(err)
			}

			attemptReconciler := &stepattempt.StepAttemptReconciler{Client: kube, Reader: kube, Scheme: scheme}
			reconciler := &WorkflowReconciler{Client: kube, Reader: kube, Scheme: scheme, Audit: recorder}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(workflow)}
			reconcile := func() {
				t.Helper()
				if _, err := approvalReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(approval)}); err != nil {
					t.Fatal(err)
				}
				if _, err := attemptReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(attempt)}); err != nil {
					t.Fatal(err)
				}
				if _, err := reconciler.Reconcile(ctx, request); err != nil {
					t.Fatal(err)
				}
				if err := kube.Get(ctx, request.NamespacedName, workflow); err != nil {
					t.Fatal(err)
				}
			}
			reconcile()
			if workflow.Status.Phase != string(v1alpha1.PhaseAwaitingApproval) || workflow.Status.ActiveAttemptRef != attempt.Name || workflow.Status.ActiveStepName != attempt.Spec.StepName || workflow.Status.ObservedGeneration != workflow.Generation {
				t.Fatalf("approval wait not reflected: %#v", workflow.Status)
			}
			version := workflow.ResourceVersion
			reconcile()
			if workflow.ResourceVersion != version {
				t.Fatal("unchanged approval wait wrote workflow status again")
			}
			var operations v1alpha1.UtilityOperationList
			if err := kube.List(ctx, &operations); err != nil {
				t.Fatal(err)
			}
			if len(operations.Items) != 0 {
				t.Fatal("merge operation started before approval attempt completed")
			}

			// Submit through the API, then consume the decision through all three reconcilers.
			action := "approve"
			if outcome == v1alpha1.PhaseFailed {
				action = "deny"
			}
			humanCtx := requestidentity.WithIdentity(ctx, requestidentity.Identity{Subject: "maintainer", Groups: []string{"maintainers"}})
			response, err := v1.NewServer(kube, recorder).SubmitApproval(humanCtx, &pb.ApprovalSubmission{
				Action: action, WorkflowId: workflow.Spec.WorkflowID, RequestUid: string(approval.UID),
			})
			if err != nil || !response.GetSuccess() {
				t.Fatalf("approval submission = %v, %v", response, err)
			}
			reconcile()
			reconcile()
			if err := kube.List(ctx, &operations); err != nil {
				t.Fatal(err)
			}
			var attempts v1alpha1.StepAttemptList
			if err := kube.List(ctx, &attempts); err != nil {
				t.Fatal(err)
			}
			var submitted, admitted audit.Event
			for _, event := range recorder.AllEvents() {
				if event.Type == "ApprovalDecisionSubmitted" {
					submitted = event
				}
				if event.Type == "ApprovalDecisionAdmitted" {
					admitted = event
				}
			}
			if submitted.ID == "" || admitted.CausationID != submitted.ID {
				t.Fatal("missing linked human submission/admission")
			}
			var payload audit.ApprovalDecisionEvidence
			if err := json.Unmarshal(admitted.Data, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Approver.SubjectId != "maintainer" || payload.AdmittedAt == nil || payload.ReviewedInputs[0].Digest != pin.Digest {
				t.Fatalf("incomplete approval evidence: %#v", payload)
			}
			if outcome == v1alpha1.PhaseSucceeded {
				binding := operations.Items[0].Spec.Approval
				if binding == nil || binding.Subject.Digest != pin.Digest || binding.Subject.ArtifactRef.UID != candidate.UID || binding.AdmissionEventID != admitted.ID || binding.DecisionRef.UID == "" {
					t.Fatalf("missing exact approval binding: %#v", binding)
				}
				if workflow.Status.Phase != string(v1alpha1.PhaseRunning) || workflow.Status.ActiveStepName != "request-merge" || workflow.Status.ActiveAttemptRef == attempt.Name || len(operations.Items) != 1 || len(attempts.Items) != 2 {
					t.Fatalf("workflow did not resume exactly once: status=%#v operations=%d attempts=%d", workflow.Status, len(operations.Items), len(attempts.Items))
				}
			} else if workflow.Status.Phase != string(v1alpha1.PhaseFailed) || len(operations.Items) != 0 || len(attempts.Items) != 1 {
				t.Fatalf("denied workflow advanced: status=%#v operations=%d attempts=%d", workflow.Status, len(operations.Items), len(attempts.Items))
			}
		})
	}
}
