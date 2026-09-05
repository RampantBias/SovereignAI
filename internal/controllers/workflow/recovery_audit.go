package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func recoveryUID(object client.Object) v1alpha1.UIDReference {
	return v1alpha1.UIDReference{Name: object.GetName(), UID: object.GetUID()}
}

func (r *WorkflowReconciler) captureWorkflowRecovery(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, failed *v1alpha1.StepAttempt, restart string) (*v1alpha1.WorkflowRecoveryEvidence, error) {
	if failed.UID == "" || failed.Spec.WorkflowRef != recoveryUID(workflow) || !metav1.IsControlledBy(failed, workflow) || failed.Status.Phase != v1alpha1.PhaseFailed {
		return nil, fmt.Errorf("invalid workflow recovery trigger %s", failed.Name)
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	evidence := &v1alpha1.WorkflowRecoveryEvidence{
		SelectedAt: metav1.NewTime(now.Truncate(time.Second)), TriggerAttempt: recoveryUID(failed),
		FromWorkflowAttempt: failed.Spec.WorkflowAttempt, MaxWorkflowAttempt: workflow.Spec.MaxWorkflowAttempt,
		FailureReason: failed.Status.FailureReason, Feedback: retryFeedbackForAttempt(failed),
	}
	var err error
	evidence.EvidenceEvents, err = r.failedAttemptEvidenceEvents(ctx, workflow, failed)
	if err != nil {
		return nil, err
	}
	if len(evidence.EvidenceEvents) == 0 {
		evidence.Missing = append(evidence.Missing, "trigger failure event")
	}
	if evidence.Feedback == nil {
		evidence.Missing = append(evidence.Missing, "retry diagnostic")
	}
	// The demo has at most one intervening utility: test -> prepared candidate -> author.
	// Follow consumed pins, never the latest output of a matching step.
	links, previous, err := r.recoveryInputPath(ctx, workflow, failed, restart, 2)
	if err != nil {
		return nil, err
	}
	evidence.InputLinks = links
	if previous != nil {
		ref := recoveryUID(previous)
		evidence.PreviousAttempt = &ref
		evidence.PreviousOutcome = previous.Status.Phase
	} else {
		evidence.Missing = append(evidence.Missing, "consumed output from prior restart-step attempt")
	}
	execution, _, err := r.recoveryExecution(ctx, workflow, failed)
	if err != nil {
		return nil, err
	}
	if execution != nil {
		var list v1alpha1.ArtifactList
		if err := r.stepAttemptReader().List(ctx, &list, client.InNamespace(workflow.Namespace)); err != nil {
			return nil, err
		}
		sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
		for i := range list.Items {
			artifact := &list.Items[i]
			if artifact.UID != "" && artifacts.ArtifactAccepted(artifact) && artifact.Spec.WorkflowRef == recoveryUID(workflow) &&
				artifact.Spec.ProducerGrantRef == recoveryUID(failed) && artifact.Spec.ProducerUID == execution.GetUID() &&
				artifact.Spec.ProducerRef == *failed.Status.ExecutionRef {
				evidence.Reports = append(evidence.Reports, recoveryArtifactPin(artifact))
			}
		}
	}
	if len(evidence.Reports) == 0 {
		evidence.Missing = append(evidence.Missing, "accepted trigger report artifact")
	}
	return evidence, nil
}

func recoveryArtifactPin(artifact *v1alpha1.Artifact) v1alpha1.ArtifactReference {
	ref := recoveryUID(artifact)
	return v1alpha1.ArtifactReference{Name: artifact.Spec.Contract.Name, Digest: artifact.Spec.Digest, ArtifactRef: &ref, ProducerAttemptRef: artifact.Spec.ProducerRef.Name}
}

// Missing historical objects leave an explicit gap. Identity mismatches are errors.
func (r *WorkflowReconciler) recoveryExecution(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, attempt *v1alpha1.StepAttempt) (client.Object, []v1alpha1.ArtifactReference, error) {
	if attempt.UID == "" || attempt.Spec.WorkflowRef != recoveryUID(workflow) || !metav1.IsControlledBy(attempt, workflow) {
		return nil, nil, fmt.Errorf("recovery attempt %s has invalid workflow ownership", attempt.Name)
	}
	ref := attempt.Status.ExecutionRef
	if ref == nil {
		return nil, nil, nil
	}
	if ref.APIVersion != v1alpha1.GroupVersion.String() || ref.Kind != controllers.DomainKind(attempt.Spec.Kind) {
		return nil, nil, fmt.Errorf("recovery execution does not match attempt %s", attempt.Name)
	}
	var object client.Object
	switch attempt.Spec.Kind {
	case v1alpha1.ExecutionKindAgent:
		object = &v1alpha1.AgentRun{}
	case v1alpha1.ExecutionKindUtility:
		object = &v1alpha1.UtilityOperation{}
	default:
		return nil, nil, nil
	}
	if err := r.stepAttemptReader().Get(ctx, types.NamespacedName{Namespace: attempt.Namespace, Name: ref.Name}, object); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var inputs []v1alpha1.ArtifactReference
	var err error
	switch value := object.(type) {
	case *v1alpha1.AgentRun:
		err = controllers.ValidateDomainBinding(attempt, value, value.Spec.AttemptRef, v1alpha1.ExecutionKindAgent, value.Spec.WorkflowRef, value.Spec.StepName, value.Spec.Attempt)
		inputs = value.Spec.Inputs
	case *v1alpha1.UtilityOperation:
		err = controllers.ValidateDomainBinding(attempt, value, value.Spec.AttemptRef, v1alpha1.ExecutionKindUtility, value.Spec.WorkflowRef, value.Spec.StepName, value.Spec.Attempt)
		inputs = value.Spec.Inputs
	}
	if err != nil {
		return nil, nil, err
	}
	if object.GetUID() == "" {
		return nil, nil, fmt.Errorf("recovery execution %s has no UID", object.GetName())
	}
	return object, inputs, nil
}

func (r *WorkflowReconciler) recoveryInputPath(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, consumer *v1alpha1.StepAttempt, restart string, depth int) ([]v1alpha1.RecoveryInputLink, *v1alpha1.StepAttempt, error) {
	if depth == 0 {
		return nil, nil, nil
	}
	_, inputs, err := r.recoveryExecution(ctx, workflow, consumer)
	if err != nil {
		return nil, nil, err
	}
	var found *v1alpha1.StepAttempt
	var links []v1alpha1.RecoveryInputLink
	for _, input := range inputs {
		if input.ArtifactRef == nil || input.ArtifactRef.UID == "" {
			continue
		}
		artifact, ready, reason, err := artifacts.ResolvePinnedInput(ctx, r.stepAttemptReader(), workflow.Namespace, recoveryUID(workflow), input)
		if err != nil {
			return nil, nil, err
		}
		if reason != "" {
			return nil, nil, fmt.Errorf("recovery input: %s", reason)
		}
		if !ready || artifact.Spec.ProducerGrantRef.UID == "" {
			continue
		}
		var producer v1alpha1.StepAttempt
		err = r.stepAttemptReader().Get(ctx, types.NamespacedName{Namespace: workflow.Namespace, Name: artifact.Spec.ProducerGrantRef.Name}, &producer)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if recoveryUID(&producer) != artifact.Spec.ProducerGrantRef || producer.Spec.WorkflowAttempt > consumer.Spec.WorkflowAttempt {
			return nil, nil, fmt.Errorf("recovery input %s has invalid producer attempt", artifact.Name)
		}
		execution, _, err := r.recoveryExecution(ctx, workflow, &producer)
		if err != nil {
			return nil, nil, err
		}
		if execution == nil {
			continue
		}
		if execution.GetUID() != artifact.Spec.ProducerUID || *producer.Status.ExecutionRef != artifact.Spec.ProducerRef {
			return nil, nil, fmt.Errorf("recovery input %s producer identity mismatch", artifact.Name)
		}
		var path []v1alpha1.RecoveryInputLink
		var prior *v1alpha1.StepAttempt
		if producer.Spec.StepName == restart {
			prior = &producer
		} else if producer.Spec.Kind == v1alpha1.ExecutionKindUtility {
			path, prior, err = r.recoveryInputPath(ctx, workflow, &producer, restart, depth-1)
			if err != nil {
				return nil, nil, err
			}
		}
		if prior == nil {
			continue
		}
		if found != nil && found.UID != prior.UID {
			return nil, nil, fmt.Errorf("recovery inputs identify multiple prior %s attempts", restart)
		}
		found = prior
		links = append(links, v1alpha1.RecoveryInputLink{Consumer: recoveryUID(consumer), Input: input, Producer: recoveryUID(&producer)})
		links = append(links, path...)
	}
	return links, found, nil
}

func workflowRecoveryEventID(workflow *v1alpha1.SovereignWorkflow) string {
	refinement := workflow.Status.Refinement
	return audit.DeterministicID("workflow-rewind/v1", string(workflow.UID), string(refinement.Recovery.TriggerAttempt.UID), fmt.Sprint(refinement.Iteration))
}

func (r *WorkflowReconciler) recordWorkflowRecovery(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) error {
	refinement := workflow.Status.Refinement
	if r.Audit == nil || refinement == nil || refinement.Recovery == nil {
		return nil
	}
	evidence := refinement.Recovery
	input, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	eventID := workflowRecoveryEventID(workflow)
	payload := audit.WorkflowRecoveryDecision{
		DecisionEvaluated: audit.DecisionEvaluated{
			SchemaVersion: audit.PayloadSchemaVersionV1, Primitive: workflowResourceRef("SovereignWorkflow", workflow),
			Decision:       audit.DecisionRef{SchemaVersion: audit.PayloadSchemaVersionV1, ID: eventID, Kind: "controller", Revision: "workflow-rewind/v1", InputDigest: artifactcontract.DigestBytes(input), Outcome: "selected"},
			EvidenceEvents: evidence.EvidenceEvents,
			Invariants: []audit.InvariantResult{
				{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "trigger-attempt-failed", Outcome: "passed", Observed: "Failed"},
				{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "workflow-attempt-budget-remaining", Outcome: "passed", Expected: fmt.Sprintf("iteration <= %d", evidence.MaxWorkflowAttempt), Observed: fmt.Sprint(refinement.Iteration)},
			},
		},
		Action: string(failureActionRetryWorkflow), RestartStep: refinement.RestartStepName, ToWorkflowAttempt: refinement.Iteration,
		NextAttemptName: attemptName(refinement.RestartStepName, refinement.Iteration, 1), Recovery: *evidence,
	}
	return r.appendWorkflowRecoveryEvent(ctx, workflow, eventID, audit.EventOptions{
		Type: "RecoveryDecisionSelected", OccurredAt: evidence.SelectedAt.Time, Action: "select", Target: refinement.RestartStepName,
		Outcome: "selected", Reason: evidence.FailureReason, DecisionID: eventID, Data: payload,
	})
}

func (r *WorkflowReconciler) recordWorkflowRetry(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, attempt *v1alpha1.StepAttempt, executionRef *v1alpha1.TypedLocalReference) error {
	refinement := workflow.Status.Refinement
	if r.Audit == nil || refinement == nil || refinement.Recovery == nil || attempt.Spec.WorkflowAttempt != refinement.Iteration || attempt.Spec.StepName != refinement.RestartStepName || attempt.Spec.RetryNumber != 1 {
		return nil
	}
	// Validate the stored execution without publishing authority to its controller yet.
	bound := attempt.DeepCopy()
	bound.Status.ExecutionRef = executionRef
	execution, _, err := r.recoveryExecution(ctx, workflow, bound)
	if err != nil {
		return err
	}
	run, ok := execution.(*v1alpha1.AgentRun)
	if !ok {
		return fmt.Errorf("workflow recovery requires a persisted AgentRun")
	}
	decisionID := workflowRecoveryEventID(workflow)
	payload := audit.WorkflowRetryRecorded{
		SchemaVersion: audit.PayloadSchemaVersionV1, Workflow: workflowResourceRef("SovereignWorkflow", workflow), DecisionEvent: decisionID,
		RetryOf: refinement.Recovery.PreviousAttempt, TriggeredBy: refinement.Recovery.TriggerAttempt,
		Attempt: workflowResourceRef("StepAttempt", attempt), Execution: workflowResourceRef("AgentRun", run), WorkflowAttempt: attempt.Spec.WorkflowAttempt,
		Feedback: run.Spec.PriorAttemptRef, Inputs: run.Spec.Inputs,
	}
	occurred := run.CreationTimestamp.Time
	if occurred.IsZero() {
		occurred = refinement.Recovery.SelectedAt.Time
	}
	return r.appendWorkflowRecoveryEvent(ctx, workflow, audit.DeterministicID("workflow-retry-created/v1", decisionID, string(attempt.UID), string(run.UID)), audit.EventOptions{
		Type: "StepAttemptRetried", OccurredAt: occurred, Action: "retry", Target: attempt.Name, Outcome: "created",
		Reason: refinement.Recovery.FailureReason, CausationID: decisionID, DecisionID: decisionID, Data: payload,
	})
}

func (r *WorkflowReconciler) appendWorkflowRecoveryEvent(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, id string, options audit.EventOptions) error {
	options.Source = "workflow-controller"
	options.Actor = audit.Actor{Kind: "Controller", ID: options.Source}
	options.Subject = audit.Subject{Project: workflow.Spec.Project.Name, Namespace: workflow.Namespace, Workflow: workflow.Name, Step: workflow.Status.Refinement.RestartStepName, Attempt: 1}
	options.CorrelationID = workflow.Name
	event, err := audit.NewEvent(options)
	if err != nil {
		return err
	}
	event.ID = id
	return r.Audit.Append(ctx, event)
}
