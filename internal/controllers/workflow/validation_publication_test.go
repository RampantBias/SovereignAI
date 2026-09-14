package workflow

import (
	"context"
	"github.com/SovereignAI/internal/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"strings"
	"testing"
)

func TestPublishedValidationResultUnblocksApproval(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	workflow := &v1alpha1.SovereignWorkflow{ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "wf", UID: "workflow-uid"}}
	workflow.Spec.Steps = []v1alpha1.StepConfig{
		{Name: "validation", Order: 1, Kind: v1alpha1.ExecutionKindValidation, Outputs: []v1alpha1.ContractReference{{Name: "validation-result", Version: "v1"}}},
		{Name: "approval", Order: 2, Kind: v1alpha1.ExecutionKindHumanGate, Inputs: []v1alpha1.ArtifactReference{{Name: "validation-result"}}, Approval: &v1alpha1.ApprovalSpec{Mode: v1alpha1.AnyOf, RequiredGroups: []string{"maintainers"}}},
	}
	ref := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}
	producer := &v1alpha1.StepAttempt{ObjectMeta: metav1.ObjectMeta{Name: "validation", Namespace: "wf", UID: "producer-uid"}, Spec: v1alpha1.StepAttemptSpec{WorkflowRef: ref, StepName: "validation", RetryNumber: 1, Kind: v1alpha1.ExecutionKindValidation}, Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhaseSucceeded}}
	consumer := &v1alpha1.StepAttempt{ObjectMeta: metav1.ObjectMeta{Name: "approval", Namespace: "wf", UID: "consumer-uid"}, Spec: v1alpha1.StepAttemptSpec{WorkflowRef: ref, StepName: "approval", RetryNumber: 1, Kind: v1alpha1.ExecutionKindHumanGate}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workflow, producer, consumer).Build()
	r := &WorkflowReconciler{Client: kube, Reader: kube, Scheme: scheme}
	if _, err := r.ensureDomainExecution(ctx, workflow, consumer, workflow.Spec.Steps[1], nil); err == nil {
		t.Fatal("approval must wait for validation artifact")
	}
	artifact := &v1alpha1.Artifact{ObjectMeta: metav1.ObjectMeta{Name: "validation-validation-result-00", Namespace: "wf", UID: "artifact-uid", Generation: 1}, Spec: v1alpha1.ArtifactSpec{WorkflowRef: ref, ProducerRef: v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ValidationRun", Name: producer.Name}, ProducerUID: "run-uid", ProducerGrantRef: v1alpha1.UIDReference{Name: producer.Name, UID: producer.UID}, Contract: v1alpha1.ContractReference{Name: "validation-result", Version: "v1"}, Digest: "sha256:" + strings.Repeat("a", 64)}, Status: v1alpha1.ArtifactStatus{Phase: v1alpha1.PhaseSucceeded, ObservedGeneration: 1, Conditions: []metav1.Condition{{Type: "Valid", Status: metav1.ConditionTrue, ObservedGeneration: 1}}}}
	if err := kube.Create(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	requests := workflowForArtifact(ctx, artifact)
	if len(requests) != 1 || requests[0].NamespacedName != (types.NamespacedName{Namespace: "wf", Name: "workflow"}) {
		t.Fatal("collector artifact did not wake workflow")
	}
	execution, err := r.ensureDomainExecution(ctx, workflow, consumer, workflow.Spec.Steps[1], nil)
	if err != nil {
		t.Fatal(err)
	}
	var request v1alpha1.ApprovalRequest
	if err := kube.Get(ctx, types.NamespacedName{Namespace: "wf", Name: execution.Name}, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Spec.Inputs) != 1 || request.Spec.Inputs[0].Digest != artifact.Spec.Digest || request.Spec.Inputs[0].ArtifactRef == nil || request.Spec.Inputs[0].ArtifactRef.UID != artifact.UID {
		t.Fatal("approval did not pin the published validation result")
	}
}
