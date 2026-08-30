package workflow

import (
	"context"
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifacts"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Input selection pins exact accepted artifacts into each immutable domain
// execution spec. Earlier workflow results are reusable only when their
// producer precedes the current workflow retry boundary.
func (r *WorkflowReconciler) resolveStepInputs(
	ctx context.Context,
	workflow *v1alpha1.SovereignWorkflow,
	consumer *v1alpha1.StepAttempt,
	step v1alpha1.StepConfig,
) ([]v1alpha1.ArtifactReference, error) {
	if len(step.Inputs) == 0 {
		return nil, nil
	}

	var artifactList v1alpha1.ArtifactList
	if err := r.Client.List(ctx, &artifactList, client.InNamespace(workflow.Namespace)); err != nil {
		return nil, fmt.Errorf("list workflow input artifacts: %w", err)
	}

	var attemptList v1alpha1.StepAttemptList
	if err := r.stepAttemptReader().List(ctx, &attemptList, client.InNamespace(workflow.Namespace)); err != nil {
		return nil, fmt.Errorf("list workflow step attempts: %w", err)
	}
	attemptsByName := make(map[string]*v1alpha1.StepAttempt, len(attemptList.Items))
	for index := range attemptList.Items {
		attempt := &attemptList.Items[index]
		if attempt.Spec.WorkflowRef == consumer.Spec.WorkflowRef {
			attemptsByName[attempt.Name] = attempt
		}
	}

	restartIndex := -1
	if consumer.Spec.WorkflowAttempt > 0 {
		refinement := workflow.Status.Refinement
		if refinement == nil || refinement.Iteration != consumer.Spec.WorkflowAttempt {
			return nil, fmt.Errorf(
				"workflow attempt %d has no matching refinement lineage",
				consumer.Spec.WorkflowAttempt,
			)
		}
		restartIndex = stepIndex(workflow.Spec.Steps, refinement.RestartStepName)
		if restartIndex < 0 {
			return nil, fmt.Errorf(
				"workflow retry target %q does not exist",
				refinement.RestartStepName,
			)
		}
	}

	resolved := make([]v1alpha1.ArtifactReference, 0, len(step.Inputs))
	seen := make(map[string]struct{}, len(step.Inputs))
	for _, requested := range step.Inputs {
		if requested.Name == "" {
			return nil, fmt.Errorf("step %q has an input with an empty artifact name", step.Name)
		}
		if _, duplicate := seen[requested.Name]; duplicate {
			return nil, fmt.Errorf("step %q declares input artifact %q more than once", step.Name, requested.Name)
		}
		seen[requested.Name] = struct{}{}

		producerIndex, bootstrap, err := workflowInputProducer(workflow, step, requested.Name)
		if err != nil {
			return nil, err
		}

		var selected *inputArtifactCandidate
		eligiblePending := false
		eligibleRejected := false
		for index := range artifactList.Items {
			artifact := &artifactList.Items[index]
			if artifact.Spec.WorkflowRef != consumer.Spec.WorkflowRef ||
				artifact.Spec.Contract.Name != requested.Name ||
				(requested.Digest != "" && artifact.Spec.Digest != requested.Digest) {
				continue
			}

			candidate, eligible := inputCandidateForArtifact(
				workflow,
				consumer,
				artifact,
				attemptsByName,
				producerIndex,
				restartIndex,
				bootstrap,
			)
			if !eligible {
				continue
			}

			switch {
			case artifacts.ArtifactAccepted(artifact):
				if selected != nil &&
					candidate.workflowAttempt == selected.workflowAttempt &&
					candidate.retryNumber == selected.retryNumber {
					return nil, fmt.Errorf(
						"input artifact %q has multiple accepted artifacts from attempt %q",
						requested.Name,
						candidate.producerAttemptRef,
					)
				}
				if selected == nil || candidate.newerThan(*selected) {
					copy := candidate
					selected = &copy
				}
			case artifact.Status.Phase == v1alpha1.PhaseFailed:
				eligibleRejected = true
			default:
				eligiblePending = true
			}
		}

		if selected == nil {
			switch {
			case eligiblePending:
				return nil, fmt.Errorf("input artifact %q is not accepted yet", requested.Name)
			case eligibleRejected:
				return nil, fmt.Errorf("input artifact %q was rejected", requested.Name)
			case requested.Digest != "":
				return nil, fmt.Errorf(
					"input artifact %q has no eligible accepted artifact with digest %q",
					requested.Name,
					requested.Digest,
				)
			default:
				return nil, fmt.Errorf(
					"input artifact %q has no eligible accepted artifact for workflow attempt %d",
					requested.Name,
					consumer.Spec.WorkflowAttempt,
				)
			}
		}
		if selected.artifact.UID == "" {
			return nil, fmt.Errorf("input artifact %q has no Kubernetes UID", requested.Name)
		}

		resolved = append(resolved, v1alpha1.ArtifactReference{
			Name:   requested.Name,
			Digest: selected.artifact.Spec.Digest,
			ArtifactRef: &v1alpha1.UIDReference{
				Name: selected.artifact.Name,
				UID:  selected.artifact.UID,
			},
			ProducerAttemptRef: selected.producerAttemptRef,
		})
	}

	return resolved, nil
}

type inputArtifactCandidate struct {
	artifact           *v1alpha1.Artifact
	producerAttemptRef string
	workflowAttempt    int32
	retryNumber        int32
}

func (candidate inputArtifactCandidate) newerThan(other inputArtifactCandidate) bool {
	return candidate.workflowAttempt > other.workflowAttempt ||
		(candidate.workflowAttempt == other.workflowAttempt &&
			candidate.retryNumber > other.retryNumber)
}

func inputCandidateForArtifact(
	workflow *v1alpha1.SovereignWorkflow,
	consumer *v1alpha1.StepAttempt,
	artifact *v1alpha1.Artifact,
	attemptsByName map[string]*v1alpha1.StepAttempt,
	producerIndex int,
	restartIndex int,
	bootstrap bool,
) (inputArtifactCandidate, bool) {
	if bootstrap {
		ref := workflow.Status.BootstrapArtifactRef
		if ref == nil ||
			artifact.Name != ref.Name ||
			artifact.UID != ref.UID ||
			artifact.Spec.ProducerRef.Kind != "SovereignWorkflow" ||
			artifact.Spec.ProducerRef.Name != workflow.Name {
			return inputArtifactCandidate{}, false
		}
		return inputArtifactCandidate{artifact: artifact}, true
	}

	producer, found := attemptsByName[artifact.Spec.ProducerRef.Name]
	if !found ||
		producer.Status.Phase != v1alpha1.PhaseSucceeded ||
		producer.Spec.StepName != workflow.Spec.Steps[producerIndex].Name ||
		producer.Spec.WorkflowAttempt > consumer.Spec.WorkflowAttempt {
		return inputArtifactCandidate{}, false
	}

	requireCurrentWorkflowAttempt := consumer.Spec.WorkflowAttempt == 0 ||
		producerIndex >= restartIndex
	if requireCurrentWorkflowAttempt &&
		producer.Spec.WorkflowAttempt != consumer.Spec.WorkflowAttempt {
		return inputArtifactCandidate{}, false
	}

	return inputArtifactCandidate{
		artifact:           artifact,
		producerAttemptRef: producer.Name,
		workflowAttempt:    producer.Spec.WorkflowAttempt,
		retryNumber:        producer.Spec.RetryNumber,
	}, true
}

func workflowInputProducer(
	workflow *v1alpha1.SovereignWorkflow,
	consumer v1alpha1.StepConfig,
	contractName string,
) (int, bool, error) {
	if workflow.Spec.Bootstrap.Contract.Name == contractName {
		return -1, true, nil
	}

	consumerIndex := stepIndex(workflow.Spec.Steps, consumer.Name)
	if consumerIndex < 0 {
		return -1, false, fmt.Errorf("consumer step %q does not exist", consumer.Name)
	}

	producerIndex := -1
	for index := 0; index < consumerIndex; index++ {
		for _, output := range workflow.Spec.Steps[index].Outputs {
			if output.Name != contractName {
				continue
			}
			if producerIndex >= 0 {
				return -1, false, fmt.Errorf(
					"input artifact %q has multiple upstream producers: %q and %q",
					contractName,
					workflow.Spec.Steps[producerIndex].Name,
					workflow.Spec.Steps[index].Name,
				)
			}
			producerIndex = index
		}
	}
	if producerIndex < 0 {
		return -1, false, fmt.Errorf(
			"input artifact %q has no upstream producer for step %q",
			contractName,
			consumer.Name,
		)
	}
	return producerIndex, false, nil
}
