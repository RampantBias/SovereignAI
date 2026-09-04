package controllers

import (
	"context"
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResolveArtifactEvidence resolves the immutable artifact identities selected
// by the workflow. A returned ready value means every requested input was
// accepted and the evidence describes the exact Kubernetes object and bytes.
func ResolveArtifactEvidence(
	ctx context.Context,
	reader client.Reader,
	namespace string,
	workflowRef v1alpha1.UIDReference,
	inputs []v1alpha1.ArtifactReference,
) (evidence []audit.ArtifactEvidence, ready bool, invalidReason string, err error) {
	evidence = make([]audit.ArtifactEvidence, 0, len(inputs))
	for _, input := range inputs {
		var artifact *v1alpha1.Artifact
		if input.ArtifactRef != nil {
			if input.ArtifactRef.Name == "" || input.ArtifactRef.UID == "" || input.Digest == "" {
				return nil, false, fmt.Sprintf("input artifact %q has an incomplete immutable pin", input.Name), nil
			}
			resolved, artifactReady, reason, err := artifacts.ResolvePinnedInput(
				ctx,
				reader,
				namespace,
				workflowRef,
				input,
			)
			if err != nil {
				return nil, false, "", err
			}
			if reason != "" {
				return nil, false, reason, nil
			}
			if !artifactReady {
				return nil, false, "", nil
			}
			artifact = resolved
		} else {
			resolved, found, reason, err := resolveUnpinnedArtifactEvidence(ctx, reader, namespace, workflowRef, input)
			if err != nil {
				return nil, false, "", err
			}
			if reason != "" {
				return nil, false, reason, nil
			}
			if !found {
				return nil, false, "", nil
			}
			artifact = resolved
		}
		if artifact.UID == "" {
			return nil, false, fmt.Sprintf("input artifact %q has no immutable Kubernetes UID", input.Name), nil
		}
		evidence = append(evidence, artifactEvidence(artifact))
	}
	return evidence, true, "", nil
}

// Legacy domain objects can lack an ArtifactRef even though their immutable
// spec still selects one accepted artifact unambiguously. Preserve that
// compatibility while recording the concrete UID and digest that were used.
func resolveUnpinnedArtifactEvidence(
	ctx context.Context,
	reader client.Reader,
	namespace string,
	workflowRef v1alpha1.UIDReference,
	input v1alpha1.ArtifactReference,
) (*v1alpha1.Artifact, bool, string, error) {
	var list v1alpha1.ArtifactList
	if err := reader.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, false, "", fmt.Errorf("list input artifacts: %w", err)
	}
	matches := make([]*v1alpha1.Artifact, 0, 1)
	for index := range list.Items {
		artifact := &list.Items[index]
		if artifact.Spec.WorkflowRef == workflowRef &&
			artifact.Spec.Contract.Name == input.Name &&
			(input.Digest == "" || artifact.Spec.Digest == input.Digest) &&
			(input.ProducerAttemptRef == "" || artifact.Spec.ProducerRef.Name == input.ProducerAttemptRef) &&
			artifacts.ArtifactAccepted(artifact) {
			matches = append(matches, artifact)
		}
	}
	if len(matches) == 0 {
		return nil, false, "", nil
	}
	if len(matches) != 1 {
		return nil, false, fmt.Sprintf("input artifact %q resolves to %d accepted artifacts", input.Name, len(matches)), nil
	}
	return matches[0], true, "", nil
}

func InputsResolvedPayload(kind string, consumer client.Object, inputs []audit.ArtifactEvidence) audit.InputsResolved {
	return audit.InputsResolved{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Consumer: audit.ResourceRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			APIVersion:    v1alpha1.GroupVersion.String(),
			Kind:          kind,
			Namespace:     consumer.GetNamespace(),
			Name:          consumer.GetName(),
			UID:           string(consumer.GetUID()),
		},
		Inputs: inputs,
	}
}

func artifactEvidence(artifact *v1alpha1.Artifact) audit.ArtifactEvidence {
	return audit.ArtifactEvidence{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Artifact: audit.ResourceRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			APIVersion:    v1alpha1.GroupVersion.String(),
			Kind:          "Artifact",
			Namespace:     artifact.Namespace,
			Name:          artifact.Name,
			UID:           string(artifact.UID),
		},
		Contract: artifact.Spec.Contract.Name + "/" + artifact.Spec.Contract.Version,
		Digest:   artifact.Spec.Digest,
		Producer: audit.ResourceRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			APIVersion:    artifact.Spec.ProducerRef.APIVersion,
			Kind:          artifact.Spec.ProducerRef.Kind,
			Namespace:     artifact.Namespace,
			Name:          artifact.Spec.ProducerRef.Name,
			UID:           string(artifact.Spec.ProducerUID),
		},
		SourceRevision: artifact.Spec.SourceRevision,
		Classification: artifact.Spec.Classification,
	}
}
