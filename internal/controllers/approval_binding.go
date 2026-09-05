package controllers

import (
	"context"
	"fmt"
	"reflect"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/domain/state"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResolveRequiredApproval resolves the named gate in this workflow iteration.
// Only the latest gate attempt can authorize the operation; an earlier approval
// cannot be substituted after a retry. The result is persisted on the operation.
func ResolveRequiredApproval(ctx context.Context, reader client.Reader, recorder audit.Recorder, workflow *v1alpha1.SovereignWorkflow, consumer *v1alpha1.StepAttempt, step v1alpha1.StepConfig, inputs []v1alpha1.ArtifactReference) (*v1alpha1.ResolvedApproval, error) {
	if err := v1alpha1.ValidateApprovalRequirements(workflow.Spec.Steps); err != nil {
		return nil, err
	}
	required := step.RequiresApproval
	if required == nil {
		return nil, nil
	}
	if workflow.UID != consumer.Spec.WorkflowRef.UID || workflow.Name != consumer.Spec.WorkflowRef.Name || !metav1.IsControlledBy(consumer, workflow) {
		return nil, fmt.Errorf("approval consumer does not belong to workflow")
	}
	var attempts v1alpha1.StepAttemptList
	if err := reader.List(ctx, &attempts, client.InNamespace(workflow.Namespace)); err != nil {
		return nil, err
	}
	var gate *v1alpha1.StepAttempt
	for i := range attempts.Items {
		item := &attempts.Items[i]
		if item.Spec.WorkflowRef != consumer.Spec.WorkflowRef || item.Spec.StepName != required.Step || item.Spec.WorkflowAttempt != consumer.Spec.WorkflowAttempt {
			continue
		}
		if gate != nil && item.Spec.RetryNumber == gate.Spec.RetryNumber {
			return nil, fmt.Errorf("ambiguous approval gate attempt")
		}
		if gate == nil || item.Spec.RetryNumber > gate.Spec.RetryNumber {
			gate = item
		}
	}
	if gate == nil || gate.UID == "" || !gate.DeletionTimestamp.IsZero() || gate.Status.Phase != v1alpha1.PhaseSucceeded || gate.Spec.Kind != v1alpha1.ExecutionKindHumanGate || !metav1.IsControlledBy(gate, workflow) || gate.Status.ExecutionRef == nil {
		return nil, fmt.Errorf("required approval gate has not succeeded in this workflow attempt")
	}
	execution := gate.Status.ExecutionRef
	if execution.Kind != "ApprovalRequest" || execution.APIVersion != v1alpha1.GroupVersion.String() {
		return nil, fmt.Errorf("invalid approval gate execution")
	}
	var request v1alpha1.ApprovalRequest
	if err := reader.Get(ctx, client.ObjectKey{Namespace: workflow.Namespace, Name: execution.Name}, &request); err != nil {
		return nil, err
	}
	if request.UID == "" || !request.DeletionTimestamp.IsZero() || request.Status.Phase != v1alpha1.PhaseSucceeded || request.Status.DecisionUID == "" || request.Status.DecisionRef == "" || request.Spec.AttemptRef != (v1alpha1.UIDReference{Name: gate.Name, UID: gate.UID}) {
		return nil, fmt.Errorf("required request has no admitted approval")
	}
	if err := ValidateDomainBinding(gate, &request, request.Spec.AttemptRef.Name, v1alpha1.ExecutionKindHumanGate, request.Spec.WorkflowRef, request.Spec.StepName, request.Spec.Attempt); err != nil {
		return nil, err
	}
	var decision v1alpha1.ApprovalDecision
	if err := reader.Get(ctx, client.ObjectKey{Namespace: workflow.Namespace, Name: request.Status.DecisionRef}, &decision); err != nil {
		return nil, err
	}
	if decision.UID != request.Status.DecisionUID || decision.Spec.Decision != v1alpha1.Approved {
		return nil, fmt.Errorf("approval decision is not the admitted approved decision")
	}
	if reason := validateApprovalDecision(&request, &decision); reason != "" {
		return nil, fmt.Errorf("invalid approval decision: %s", reason)
	}
	reviewed, err := pinnedApprovalSubject(request.Spec.Inputs, required.Subject)
	if err != nil {
		return nil, err
	}
	operated, err := pinnedApprovalSubject(inputs, required.Subject)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(reviewed.ArtifactRef, operated.ArtifactRef) || reviewed.Digest != operated.Digest {
		return nil, fmt.Errorf("operation subject does not match the approved artifact UID and digest")
	}
	// Resolve actual accepted artifacts, rather than trusting a matching pair of
	// user-provided pins. All review evidence must still be valid at authorization.
	for _, selected := range [][]v1alpha1.ArtifactReference{request.Spec.Inputs, inputs} {
		_, ready, invalid, err := ResolveArtifactEvidence(ctx, reader, workflow.Namespace, consumer.Spec.WorkflowRef, selected)
		if err != nil {
			return nil, err
		}
		if invalid != "" || !ready {
			return nil, fmt.Errorf("approval evidence is unavailable or invalid: %s", invalid)
		}
	}
	if err := audit.RecordApprovalAdmission(ctx, recorder, &request, &decision); err != nil {
		return nil, err
	}
	return &v1alpha1.ResolvedApproval{Requirement: *required,
		RequestRef:  v1alpha1.UIDReference{Name: request.Name, UID: request.UID},
		DecisionRef: v1alpha1.UIDReference{Name: decision.Name, UID: decision.UID}, Subject: reviewed,
		AdmissionEventID: audit.ApprovalAdmissionEventID(&request)}, nil
}

func pinnedApprovalSubject(inputs []v1alpha1.ArtifactReference, name string) (v1alpha1.ArtifactReference, error) {
	var result *v1alpha1.ArtifactReference
	for i := range inputs {
		if inputs[i].Name == name {
			if result != nil {
				return v1alpha1.ArtifactReference{}, fmt.Errorf("ambiguous approval subject %q", name)
			}
			result = &inputs[i]
		}
	}
	if result == nil || result.ArtifactRef == nil || result.ArtifactRef.Name == "" || result.ArtifactRef.UID == "" || result.Digest == "" {
		return v1alpha1.ArtifactReference{}, fmt.Errorf("approval subject %q requires an exact artifact pin", name)
	}
	return *result.DeepCopy(), nil
}

// ValidateOperationApproval re-resolves the declared authority before policy
// evaluation and execution. Operation fields cannot manufacture approval facts.
func ValidateOperationApproval(ctx context.Context, reader client.Reader, recorder audit.Recorder, operation *v1alpha1.UtilityOperation, workflow *v1alpha1.SovereignWorkflow) error {
	var step *v1alpha1.StepConfig
	for i := range workflow.Spec.Steps {
		if workflow.Spec.Steps[i].Name == operation.Spec.StepName {
			step = &workflow.Spec.Steps[i]
			break
		}
	}
	if step == nil {
		return fmt.Errorf("utility step is absent from workflow")
	}
	if step.RequiresApproval == nil && operation.Spec.Approval == nil && operation.Spec.Operation.Name != "git.mergeRequest" {
		return nil
	}
	if step.RequiresApproval == nil || operation.Spec.Approval == nil {
		return fmt.Errorf("utility requires explicit resolved approval")
	}
	if step.Utility == nil || step.Utility.Name != operation.Spec.Operation.Name {
		return fmt.Errorf("operation differs from declared utility")
	}
	var consumer v1alpha1.StepAttempt
	if err := reader.Get(ctx, client.ObjectKey{Namespace: operation.Namespace, Name: operation.Spec.AttemptRef}, &consumer); err != nil {
		return err
	}
	if state.IsTerminal(consumer.Status.Phase) || consumer.Spec.WorkflowAttempt != workflow.Status.WorkflowAttempt {
		return fmt.Errorf("approval consumer is no longer current")
	}
	resolved, err := ResolveRequiredApproval(ctx, reader, recorder, workflow, &consumer, *step, operation.Spec.Inputs)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(resolved, operation.Spec.Approval) {
		return fmt.Errorf("operation approval binding differs from admitted authority")
	}
	return nil
}
