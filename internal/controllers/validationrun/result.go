package validationrun

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/validation"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ValidationRunReconciler) resultTime() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *ValidationRunReconciler) buildValidationResult(ctx context.Context, run *v1alpha1.ValidationRun, status validation.Status) (artifactcontract.ValidationResult, error) {
	var result artifactcontract.ValidationResult
	project, err := r.validationProject(ctx, run)
	if err != nil {
		return result, err
	}
	subject, resolution, message, err := r.resolveValidationSubject(ctx, run, project)
	if err != nil {
		return result, err
	}
	if resolution != "ready" {
		return result, fmt.Errorf("validation result inputs %s: %s", resolution, message)
	}
	if !status.Ready || status.SyncStatus != "Synced" || status.HealthStatus != "Healthy" {
		return result, fmt.Errorf("validation result requires Synced/Healthy Argo status; observed %s/%s", status.SyncStatus, status.HealthStatus)
	}
	if status.ObservedRevision != subject.commit {
		return result, fmt.Errorf("validation source revision mismatch: expected %s, observed %s", subject.commit, status.ObservedRevision)
	}
	if !slices.Contains(status.ObservedImages, subject.imageReference) {
		return result, fmt.Errorf("validation image evidence missing or mismatched: expected %s, observed %v", subject.imageReference, status.ObservedImages)
	}
	if status.ApplicationName != run.Status.ProviderRef || status.ApplicationUID == "" || status.ApplicationNamespace == "" || status.DestinationNamespace != run.Namespace {
		return result, fmt.Errorf("validation result application identity or destination does not match the run")
	}
	candidate, _, _, err := r.resolveValidationArtifact(ctx, run, artifactcontract.CandidateRevisionContract)
	if err != nil {
		return result, err
	}
	image, _, _, err := r.resolveValidationArtifact(ctx, run, artifactcontract.ImageDigestContract)
	if err != nil {
		return result, err
	}
	if candidate == nil || image == nil {
		return result, fmt.Errorf("validation result inputs are no longer accepted")
	}
	// This v1 result contract describes the calculator preview and its six checks.
	var service corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: "calculator"}, &service); err != nil {
		return result, err
	}
	if len(service.Spec.Selector) == 0 || service.Spec.Type == corev1.ServiceTypeExternalName {
		return result, fmt.Errorf("calculator validation requires a pod-selecting internal Service")
	}
	port := int32(0)
	for _, p := range service.Spec.Ports {
		if p.Name == "http" {
			port = p.Port
		}
	}
	if port == 0 {
		return result, fmt.Errorf("calculator Service has no http port")
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(run.Namespace), client.MatchingLabels(service.Spec.Selector)); err != nil {
		return result, err
	}
	readyPods := 0
	for _, pod := range pods.Items {
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready {
			continue
		}
		matched := false
		for _, container := range pod.Spec.Containers {
			if container.Image == subject.imageReference {
				matched = true
			}
		}
		if !matched {
			return result, fmt.Errorf("calculator Service selects a ready pod with a different candidate image")
		}
		readyPods++
	}
	if readyPods == 0 {
		return result, fmt.Errorf("calculator Service has no ready candidate pods")
	}
	started := r.resultTime()
	checks, err := r.checkCalculatorEndpoints(ctx, fmt.Sprintf("http://%s.%s.svc:%d", service.Name, service.Namespace, port))
	if err != nil {
		return result, err
	}
	outcome := "passed"
	for _, check := range checks {
		if check.Outcome != "passed" {
			outcome = "failed"
		}
	}
	result = artifactcontract.ValidationResult{
		ValidationRun:           artifactcontract.ObjectIdentity{Namespace: run.Namespace, Name: run.Name, UID: string(run.UID)},
		CandidateRevisionDigest: candidate.Spec.Digest, ImageArtifactDigest: image.Spec.Digest,
		CandidateCommit: subject.commit, CandidateTree: subject.tree,
		ImageRepository: image.Spec.Claims.ImageDigest.ImageRepository, ImageDigest: image.Spec.Claims.ImageDigest.OCIDigest,
		InfrastructureRevision: subject.commit, Provider: "argocd/kustomize",
		Application:            artifactcontract.ObjectIdentity{Namespace: status.ApplicationNamespace, Name: status.ApplicationName, UID: status.ApplicationUID},
		ObservedSourceRevision: status.ObservedRevision, ObservedImageDigest: image.Spec.Claims.ImageDigest.OCIDigest,
		SyncStatus: status.SyncStatus, HealthStatus: status.HealthStatus, EndpointChecks: checks,
		ReviewInstructions: artifactcontract.ReviewInstructions{Namespace: run.Namespace, Service: service.Name, LocalPort: 8080, HealthPath: "/healthz", CalculationPath: "/api/v1/calculate"},
		StartedAt:          started.Format(time.RFC3339), CompletedAt: r.resultTime().Format(time.RFC3339), Outcome: outcome,
	}
	return result, result.Validate()
}

func decodeValidationResult(content []byte, run *v1alpha1.ValidationRun) (artifactcontract.ValidationResult, error) {
	var result artifactcontract.ValidationResult
	if err := artifactcontract.ValidateContract(artifactcontract.ValidationResultContract, content); err != nil {
		return result, err
	}
	if err := json.Unmarshal(content, &result); err != nil {
		return result, err
	}
	if result.ValidationRun != (artifactcontract.ObjectIdentity{Namespace: run.Namespace, Name: run.Name, UID: string(run.UID)}) {
		return result, fmt.Errorf("stored validation result belongs to a different run")
	}
	for _, pair := range []struct{ name, digest string }{{"candidate-revision", result.CandidateRevisionDigest}, {"image-digest", result.ImageArtifactDigest}} {
		found := false
		for _, input := range run.Spec.Inputs {
			if input.Name == pair.name && input.Digest == pair.digest {
				found = true
			}
		}
		if !found {
			return result, fmt.Errorf("stored validation result does not match pinned %s", pair.name)
		}
	}
	return result, nil
}
