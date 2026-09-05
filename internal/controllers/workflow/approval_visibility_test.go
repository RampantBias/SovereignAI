package workflow

import (
	"context"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
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
			workflow.Generation = 1
			workflow.Spec.Steps = []v1alpha1.StepConfig{
				{Name: "product-approval", Kind: v1alpha1.ExecutionKindHumanGate, Order: 1, MaxAttempts: 1,
					Approval: &v1alpha1.ApprovalSpec{Mode: v1alpha1.AnyOf, RequiredGroups: []string{"maintainers"}, DenyBehavior: "Fail"}},
				{Name: "request-merge", Kind: v1alpha1.ExecutionKindUtility, Order: 2,
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
					Approval: *workflow.Spec.Steps[0].Approval,
				},
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.StepAttempt{}, &v1alpha1.ApprovalRequest{}, &v1alpha1.UtilityOperation{}).
				WithObjects(workflow, changeRequestArtifact(workflow), attempt, approval).Build()
			approvalReconciler := &controllers.ApprovalRequestReconciler{Client: kube}
			if _, err := approvalReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(approval)}); err != nil {
				t.Fatal(err)
			}

			attemptReconciler := &stepattempt.StepAttemptReconciler{Client: kube, Reader: kube, Scheme: scheme}
			reconciler := &WorkflowReconciler{Client: kube, Reader: kube, Scheme: scheme}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(workflow)}
			reconcile := func() {
				t.Helper()
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

			// Decision consumption is a separate change. Simulate its ApprovalRequest result,
			// then let the real StepAttempt and Workflow reconcilers propagate it.
			if err := kube.Get(ctx, client.ObjectKeyFromObject(approval), approval); err != nil {
				t.Fatal(err)
			}
			approval.Status.Phase = outcome
			if outcome == v1alpha1.PhaseFailed {
				approval.Status.FailureReason = "ApprovalDenied"
			}
			if err := kube.Status().Update(ctx, approval); err != nil {
				t.Fatal(err)
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
			if outcome == v1alpha1.PhaseSucceeded {
				if workflow.Status.Phase != string(v1alpha1.PhaseRunning) || workflow.Status.ActiveStepName != "request-merge" || workflow.Status.ActiveAttemptRef == attempt.Name || len(operations.Items) != 1 || len(attempts.Items) != 2 {
					t.Fatalf("workflow did not resume exactly once: status=%#v operations=%d attempts=%d", workflow.Status, len(operations.Items), len(attempts.Items))
				}
			} else if workflow.Status.Phase != string(v1alpha1.PhaseFailed) || len(operations.Items) != 0 || len(attempts.Items) != 1 {
				t.Fatalf("denied workflow advanced: status=%#v operations=%d attempts=%d", workflow.Status, len(operations.Items), len(attempts.Items))
			}
		})
	}
}
