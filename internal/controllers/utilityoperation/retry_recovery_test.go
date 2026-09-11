package utilityoperation

import (
	"context"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAdmissionEventsDistinguishWorkflowIterationsAndRepairMissingEvidence(t *testing.T) {
	ctx := context.Background()
	scheme := attemptScheme(t)
	workflow := workflowFixture()
	project := projectFixture()
	first := &v1alpha1.UtilityOperation{}
	first.Name, first.Namespace, first.UID = "prepare-candidate-w000-r001", workflow.Namespace, "first-operation"
	first.Spec = v1alpha1.UtilityOperationSpec{WorkflowRef: workflowRef(workflow), StepName: "prepare-candidate", Attempt: 1, Operation: v1alpha1.UtilityOperationRequest{Name: "candidate.prepare"}}
	second := first.DeepCopy()
	second.Name, second.UID = "prepare-candidate-w001-r001", "second-operation"
	// Reproduce an already-stalled resource: policy status exists, admission does not.
	second.Status.PolicyDecisionID = "allow-test"
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.UtilityOperation{}).WithObjects(workflow, project, first, second).Build()
	recorder := audit.NewMemoryRecorder()
	r := &UtilityOperationReconciler{Client: kube, Scheme: scheme, Policy: allowPolicy{}, Audit: recorder}
	for _, op := range []*v1alpha1.UtilityOperation{first, second, second} {
		denied, _, err := r.ensureValidOperation(ctx, op)
		if denied || err != nil {
			t.Fatalf("admission: denied=%v err=%v", denied, err)
		}
	}
	a, err := r.findOperationEventID(ctx, first, "UtilityOperationAdmitted")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.findOperationEventID(ctx, second, "UtilityOperationAdmitted")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("workflow iterations shared an admission identity")
	}
	count := 0
	for _, e := range recorder.AllEvents() {
		if e.Type == "UtilityOperationAdmitted" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("admission reconciliation duplicated evidence: %d", count)
	}
	// Recreated resources with the same name also need their own identity.
	second.UID = types.UID("replacement-operation")
	e, err := r.appendEventWithDataResult(ctx, second, "UtilityOperationAdmitted", "admit", "candidate.prepare", "admitted", "", nil)
	if err != nil || e.ID == b {
		t.Fatalf("resource UID not represented in identity: %v", err)
	}
}

func TestUtilityIdempotencyStableWithinIteration(t *testing.T) {
	workflow := workflowFixture()
	op := &v1alpha1.UtilityOperation{}
	op.Namespace = workflow.Namespace
	op.Spec.StepName = "prepare-candidate"
	original := utilityIdempotencyKey(op, workflow)
	workflow.Status.WorkflowAttempt = 1
	first := utilityIdempotencyKey(op, workflow)
	op.Spec.Attempt = 2
	op.Name = "prepare-candidate-w001-r002"
	if utilityIdempotencyKey(op, workflow) != first || first == original {
		t.Fatal("incorrect workflow/step retry idempotency")
	}
	workflow.Status.WorkflowAttempt = 2
	if utilityIdempotencyKey(op, workflow) == first {
		t.Fatal("second rewind reused candidate identity")
	}
}
