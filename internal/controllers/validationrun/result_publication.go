package validationrun

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/validation"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const resultDigestAnnotation = "sovereign-ai.io/validation-result-digest"

func (r *ValidationRunReconciler) publishValidationResult(ctx context.Context, run *v1alpha1.ValidationRun, status *validation.Status) (bool, string, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return false, "", err
	}
	if workflow.UID != run.Spec.WorkflowRef.UID {
		return false, "", fmt.Errorf("validation workflow UID mismatch")
	}
	declared := false
	for _, step := range workflow.Spec.Steps {
		if step.Name == run.Spec.StepName {
			for _, output := range step.Outputs {
				if output.Name == "validation-result" {
					if output.Version != "v1" {
						return false, "", fmt.Errorf("unsupported validation-result version")
					}
					declared = true
				}
			}
		}
	}
	// Legacy workflows may use a validation step without a declared output.
	if !declared {
		return true, "passed", nil
	}
	configKey := types.NamespacedName{Namespace: run.Namespace, Name: run.Name + "-result"}
	var config corev1.ConfigMap
	err := r.Get(ctx, configKey, &config)
	if apierrors.IsNotFound(err) {
		result, err := r.buildValidationResult(ctx, run, *status)
		if err != nil {
			return false, "", err
		}
		content, err := json.Marshal(result)
		if err != nil {
			return false, "", err
		}
		if _, err := decodeValidationResult(content, run); err != nil {
			return false, "", err
		}
		envelope, err := json.Marshal(agentcontract.Result{SchemaVersion: agentcontract.Version, Outcome: "Succeeded", Message: "validation result observed", Artifacts: []agentcontract.ArtifactOutput{{Contract: artifactcontract.ValidationResultContract, Path: "/validation-input/validation-result.json", MediaType: "application/json"}}})
		if err != nil {
			return false, "", err
		}
		immutable := true
		config = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configKey.Name, Namespace: configKey.Namespace}, Immutable: &immutable, Data: map[string]string{"validation-result.json": string(content), "result.json": string(envelope)}}
		if err := controllerutil.SetControllerReference(run, &config, r.Scheme()); err != nil {
			return false, "", err
		}
		if err := r.Create(ctx, &config); err != nil {
			return false, "", err
		}
	} else if err != nil {
		return false, "", err
	}
	if !metav1.IsControlledBy(&config, run) || config.Immutable == nil || !*config.Immutable {
		return false, "", fmt.Errorf("validation result ConfigMap has invalid ownership or is mutable")
	}
	content := []byte(config.Data["validation-result.json"])
	result, err := decodeValidationResult(content, run)
	if err != nil {
		return false, "", err
	}
	digest := artifactcontract.DigestBytes(content)
	status.ValidationResultDigest = digest
	var job batchv1.Job
	jobKey := types.NamespacedName{Namespace: run.Namespace, Name: run.Name + "-collect"}
	jobErr := r.Get(ctx, jobKey, &job)
	if jobErr != nil && !apierrors.IsNotFound(jobErr) {
		return false, "", jobErr
	}
	if jobErr == nil {
		if !metav1.IsControlledBy(&job, run) || job.Annotations[resultDigestAnnotation] != digest {
			return false, "", fmt.Errorf("validation collector identity does not match frozen result")
		}
		if job.Status.Active == 0 && job.Status.Failed > 0 {
			if err := r.releaseResultWriter(ctx, run); err != nil {
				return false, "", err
			}
			return true, "ValidationResultCollectionFailed", nil
		}
		if job.Status.Active == 0 && job.Status.Succeeded > 0 {
			var artifact v1alpha1.Artifact
			key := types.NamespacedName{Namespace: run.Namespace, Name: run.Name + "-validation-result-00"}
			if err := r.Get(ctx, key, &artifact); err != nil {
				if apierrors.IsNotFound(err) {
					return false, "", nil
				}
				return false, "", err
			}
			owner := metav1.GetControllerOf(run)
			if owner == nil {
				return false, "", fmt.Errorf("validation run has no producer grant")
			}
			expectedPath := "/workspace/.sovereign/artifacts/" + digest[len("sha256:"):]
			// The collector stores exactly the same content-addressed format used by other steps.
			if artifact.Spec.Digest != digest || artifact.Spec.WorkflowRef != run.Spec.WorkflowRef || artifact.Spec.ProducerUID != run.UID || artifact.Spec.ProducerRef.Kind != "ValidationRun" || artifact.Spec.ProducerRef.Name != run.Name || artifact.Spec.ProducerGrantRef != (v1alpha1.UIDReference{Name: owner.Name, UID: owner.UID}) || artifact.Spec.Contract != (v1alpha1.ContractReference{Name: "validation-result", Version: "v1"}) || artifact.Spec.Path != expectedPath {
				return false, "", fmt.Errorf("published validation artifact does not match the frozen result and producer")
			}
			if artifact.Status.Phase == v1alpha1.PhaseFailed {
				if err := r.releaseResultWriter(ctx, run); err != nil {
					return false, "", err
				}
				return true, "ValidationResultRejected", nil
			}
			if !artifacts.ArtifactAccepted(&artifact) {
				return false, "", nil
			}
			if err := r.releaseResultWriter(ctx, run); err != nil {
				return false, "", err
			}
			return true, result.Outcome, nil
		}
	}
	expectedEpoch := int32(0)
	if raw := config.Annotations[controllers.AnnotationWorkspaceWriterEpoch]; raw != "" {
		epoch, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || epoch < 1 {
			return false, "", fmt.Errorf("invalid validation writer epoch")
		}
		expectedEpoch = int32(epoch)
	}
	if workflow.Status.WorkspaceWriterLeaseRef == "" || workflow.Status.PvcName == "" {
		return false, "", fmt.Errorf("validation publication requires workflow workspace and writer lease")
	}
	grant, state, err := controllers.AcquireWorkspaceWriter(ctx, r.Client, &workflow, workflow.Status.WorkspaceWriterLeaseRef, "ValidationRun", run, expectedEpoch, r.resultTime())
	if err != nil {
		return false, "", err
	}
	if state == controllers.WorkspaceWriterBlocked {
		return false, "", nil
	}
	if state == controllers.WorkspaceWriterLost {
		quiet, err := r.cleanupResultPublication(ctx, run)
		if err != nil {
			return false, "", err
		}
		if !quiet {
			return false, "", nil
		}
		return true, "ValidationWriterAuthorityLost", nil
	}
	if expectedEpoch == 0 {
		before := config.DeepCopy()
		config.Annotations = controllers.WorkspaceWriterAnnotations(grant)
		if err := r.Patch(ctx, &config, client.MergeFrom(before)); err != nil {
			return false, "", err
		}
	}
	if jobErr == nil {
		return false, "", nil
	}
	// Record evaluation before the collector can publish an accepted consequence.
	evaluation := *status
	evaluation.Ready = result.Outcome == "passed"
	if err := r.appendValidationEvaluated(ctx, run, evaluation); err != nil {
		return false, "", err
	}
	image := r.CollectorImage
	if image == "" {
		image = "sovereign-artifact-collector:dev"
	}
	for _, object := range controllers.BuildCollectorResources(run, &workflow, run.Spec.StepName, image, grant) {
		if job, ok := object.(*batchv1.Job); ok {
			job.Annotations[resultDigestAnnotation] = digest
			deadline := int64(300)
			job.Spec.ActiveDeadlineSeconds = &deadline
			container := &job.Spec.Template.Spec.Containers[0]
			for i := 0; i+1 < len(container.Args); i += 2 {
				switch container.Args[i] {
				case "--result":
					container.Args[i+1] = "/validation-input/result.json"
				case "--staging":
					container.Args[i+1] = "/validation-input"
				case "--audit-events":
					container.Args[i+1] = ""
				case "--source-revision":
					container.Args[i+1] = result.CandidateCommit
				}
			}
			disabled, enabled := false, true
			container.SecurityContext = &corev1.SecurityContext{AllowPrivilegeEscalation: &disabled, ReadOnlyRootFilesystem: &enabled, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
			job.Spec.Template.Spec.SecurityContext.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
			for _, name := range []string{"result.json", "validation-result.json"} {
				container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "validation-result", MountPath: "/validation-input/" + name, SubPath: name, ReadOnly: true})
			}
			job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{Name: "validation-result", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: config.Name}}}})
		}
		if err := controllerutil.SetControllerReference(run, object, r.Scheme()); err != nil {
			return false, "", err
		}
		if err := r.Create(ctx, object); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return false, "", err
			}
			existing := object.DeepCopyObject().(client.Object)
			if err := r.Get(ctx, client.ObjectKeyFromObject(object), existing); err != nil {
				return false, "", err
			}
			if !metav1.IsControlledBy(existing, run) {
				return false, "", fmt.Errorf("validation publication resource has a different owner")
			}
			// Never reuse a Job with different inputs after an interrupted create.
			if _, ok := object.(*batchv1.Job); ok && !reflect.DeepEqual(existing.GetAnnotations(), object.GetAnnotations()) {
				return false, "", fmt.Errorf("validation collector annotations differ")
			}
		}
	}
	return false, "", nil
}

func (r *ValidationRunReconciler) releaseResultWriter(ctx context.Context, run *v1alpha1.ValidationRun) error {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return client.IgnoreNotFound(err)
	}
	if workflow.UID != run.Spec.WorkflowRef.UID || workflow.Status.WorkspaceWriterLeaseRef == "" {
		return nil
	}
	var lease coordinationv1.Lease
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: workflow.Status.WorkspaceWriterLeaseRef}, &lease); err != nil {
		return client.IgnoreNotFound(err)
	}
	holder, err := controllers.WorkspaceWriterIdentity("ValidationRun", run)
	if err != nil {
		return err
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != holder || lease.Spec.LeaseTransitions == nil {
		return nil
	}
	return controllers.ReleaseWorkspaceWriter(ctx, r.Client, run.Namespace, controllers.WorkspaceWriterGrant{LeaseName: lease.Name, HolderIdentity: holder, Epoch: *lease.Spec.LeaseTransitions}, r.resultTime())
}

func (r *ValidationRunReconciler) cleanupResultPublication(ctx context.Context, run *v1alpha1.ValidationRun) (bool, error) {
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Name + "-collect"}, &job); err == nil {
		if !metav1.IsControlledBy(&job, run) {
			return false, fmt.Errorf("cannot clean up another run's collector")
		}
		if job.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
				return false, err
			}
		}
		return false, nil
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	return true, r.releaseResultWriter(ctx, run)
}
