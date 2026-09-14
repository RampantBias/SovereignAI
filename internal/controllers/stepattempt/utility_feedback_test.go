package stepattempt

import (
	"context"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/utilitycontract"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestStepAttemptMirrorsUtilityFailureDiagnostic(t *testing.T) {
	scheme := attemptScheme(t)
	attempt := authorizedAttempt("test-candidate-w001-r001", "wf", "test-candidate", v1alpha1.ExecutionKindUtility)
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: attempt.Name, Namespace: attempt.Namespace},
		Spec:       v1alpha1.UtilityOperationSpec{AttemptRef: attempt.Name, WorkflowRef: attempt.Spec.WorkflowRef, StepName: attempt.Spec.StepName, Attempt: 1},
		Status: v1alpha1.UtilityOperationStatus{Phase: v1alpha1.PhaseFailed, FailureReason: utilitycontract.TestRunCodeError,
			FailureMessage: "main_test.go:42: expected 2, got 3", Retryable: true},
	}
	ownByAttempt(operation, attempt)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.StepAttempt{}).WithObjects(attempt, operation).Build()
	r := &StepAttemptReconciler{Client: kube, Reader: kube, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), requestFor(attempt)); err != nil {
		t.Fatal(err)
	}
	var updated v1alpha1.StepAttempt
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(attempt), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != v1alpha1.PhaseFailed || updated.Status.FailureReason != operation.Status.FailureReason ||
		updated.Status.FailureMessage != operation.Status.FailureMessage || !updated.Status.Retryable {
		t.Fatalf("attempt lost utility diagnostics: %#v", updated.Status)
	}
}
