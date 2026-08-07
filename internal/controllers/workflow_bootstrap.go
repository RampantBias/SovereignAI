package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	bootstrapArtifactRoot = "/workspace/.sovereign/artifacts"
	bootstrapMountRoot    = "/bootstrap"
	defaultBootstrapImage = "sovereign-artifact-bootstrap:dev"
)

func (r *WorkflowReconciler) reconcileBootstrap(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) (bool, ctrl.Result, error) {
	if err := validateBootstrapSpec(workflow); err != nil {
		return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "InvalidBootstrap", err.Error())
	}
	// Ensure bootstrap is ready
	if apiMeta.IsStatusConditionTrue(workflow.Status.Conditions, "BootstrapReady") {
		artifact, err := r.acceptedBootstrapArtifact(ctx, workflow)
		if err != nil {
			return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "InvalidBootstrapArtifact", err.Error())
		}
		if err := r.cleanupBootstrapResources(ctx, workflow); err != nil {
			return false, ctrl.Result{}, err
		}
		return artifact != nil, ctrl.Result{}, nil
	}

	var artifact v1alpha1.Artifact
	artifactKey := types.NamespacedName{Namespace: workflow.Namespace, Name: workflow.Spec.Bootstrap.ArtifactName}
	artifactErr := r.Get(ctx, artifactKey, &artifact)
	if artifactErr != nil && !apierrors.IsNotFound(artifactErr) {
		return false, ctrl.Result{}, artifactErr
	}
	if artifactErr == nil {
		if err := validateBootstrapArtifactIdentity(workflow, &artifact); err != nil {
			return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "InvalidBootstrapArtifact", err.Error())
		}
		if err := r.releaseBootstrapWriter(ctx, workflow); err != nil {
			return false, ctrl.Result{}, err
		}
		switch artifact.Status.Phase {
		case v1alpha1.PhaseSucceeded:
			if !bootstrapArtifactAccepted(&artifact) {
				return false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			if err := r.completeBootstrap(ctx, workflow, &artifact); err != nil {
				return false, ctrl.Result{}, err
			}
			return true, ctrl.Result{}, nil
		case v1alpha1.PhaseFailed:
			return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "BootstrapArtifactRejected", artifactConditionMessage(&artifact))
		default:
			return false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	}

	if _, _, err := loadBootstrapSource(ctx, r.Client, workflow); err != nil {
		return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "InvalidBootstrapInput", err.Error())
	}

	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	grant, state, err := acquireWorkspaceWriter(
		ctx, r.Client, workflow, workflow.Status.WorkspaceWriterLeaseRef,
		"WorkflowBootstrap", workflow, workflow.Status.BootstrapWriterEpoch, now,
	)
	if err != nil {
		return false, ctrl.Result{}, err
	}
	switch state {
	case workspaceWriterBlocked:
		return false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	case workspaceWriterLost:
		return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "BootstrapWriterLost", "bootstrap lost its fenced workspace writer grant")
	}
	if workflow.Status.BootstrapWriterEpoch != grant.Epoch || workflow.Status.BootstrapWriterReleased {
		if _, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
			latest.Status.BootstrapWriterEpoch = grant.Epoch
			latest.Status.BootstrapWriterReleased = false
		}); err != nil {
			return false, ctrl.Result{}, err
		}
		return false, ctrl.Result{Requeue: true}, nil
	}

	jobName := bootstrapJobName(workflow.Name)
	var job batchv1.Job
	jobKey := types.NamespacedName{Namespace: workflow.Namespace, Name: jobName}
	err = r.Get(ctx, jobKey, &job)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, ctrl.Result{}, err
	}
	if apierrors.IsNotFound(err) {
		image := r.BootstrapImage
		if image == "" {
			image = defaultBootstrapImage
		}
		job = *buildBootstrapJob(workflow, image, grant)
		if err := controllerutil.SetControllerReference(workflow, &job, r.Scheme); err != nil {
			return false, ctrl.Result{}, err
		}
		if err := r.Create(ctx, &job); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, ctrl.Result{}, err
		}
		updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
			latest.Status.BootstrapJobRef = jobName
		})
		if err != nil {
			return false, ctrl.Result{}, err
		}
		if err := r.appendWorkflowEvent(ctx, updated, "WorkflowBootstrapStarted", "", 0, "create", jobName, "started", "",
			map[string]string{"job": jobName, "digest": workflow.Spec.Bootstrap.ExpectedDigest}, nil); err != nil {
			return false, ctrl.Result{}, err
		}
		return false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if !metav1.IsControlledBy(&job, workflow) {
		return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "InvalidBootstrapJob", fmt.Sprintf("job %s is not controlled by workflow", job.Name))
	}
	if workflow.Status.BootstrapJobRef != job.Name {
		updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
			latest.Status.BootstrapJobRef = job.Name
		})
		if err != nil {
			return false, ctrl.Result{}, err
		}
		workflow.Status = updated.Status
	}
	if job.Status.Failed > 0 {
		if err := r.releaseBootstrapWriter(ctx, workflow); err != nil {
			return false, ctrl.Result{}, err
		}
		return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "BootstrapJobFailed", "bootstrap storage job failed")
	}
	if job.Status.Succeeded == 0 {
		return false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.releaseBootstrapWriter(ctx, workflow); err != nil {
		return false, ctrl.Result{}, err
	}

	_, changeRequest, err := loadBootstrapSource(ctx, r.Client, workflow)
	if err != nil {
		return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "InvalidBootstrapInput", err.Error())
	}
	artifact = v1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workflow.Spec.Bootstrap.ArtifactName,
			Namespace: workflow.Namespace,
			Labels: map[string]string{
				LabelWorkflow:                  workflow.Spec.WorkflowID,
				"sovereign-ai.io/bootstrap":    "true",
				"sovereign-ai.io/contribution": "contributing",
			},
		},
		Spec: v1alpha1.ArtifactSpec{
			WorkflowRef: v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.ObjectMeta.UID},
			ProducerRef: v1alpha1.TypedLocalReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       "SovereignWorkflow",
				Name:       workflow.Name,
			},
			Contract:       workflow.Spec.Bootstrap.Contract,
			Digest:         workflow.Spec.Bootstrap.ExpectedDigest,
			Path:           bootstrapArtifactPath(workflow.Spec.Bootstrap.ExpectedDigest),
			Classification: workflow.Spec.Classification,
			SourceRevision: changeRequest.SourceCommit,
		},
	}
	if err := controllerutil.SetControllerReference(workflow, &artifact, r.Scheme); err != nil {
		return false, ctrl.Result{}, err
	}
	if err := r.Create(ctx, &artifact); err != nil && !apierrors.IsAlreadyExists(err) {
		return false, ctrl.Result{}, err
	}
	if err := r.appendWorkflowEvent(ctx, workflow, "WorkflowBootstrapArtifactCreated", "", 0, "create", artifact.Name, "created", "",
		map[string]string{"artifact": artifact.Name, "digest": artifact.Spec.Digest, "job": job.Name}, nil); err != nil {
		return false, ctrl.Result{}, err
	}
	return false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func validateBootstrapSpec(workflow *v1alpha1.SovereignWorkflow) error {
	bootstrap := workflow.Spec.Bootstrap
	if bootstrap.SourceRef.Name == "" || bootstrap.SourceRef.UID == "" {
		return fmt.Errorf("bootstrap source requires ConfigMap name and UID")
	}
	if bootstrap.Key == "" || strings.ContainsAny(bootstrap.Key, `/\`) {
		return fmt.Errorf("bootstrap source key must be one filename")
	}
	if bootstrap.Contract.Name != "change-request" || bootstrap.Contract.Version != "v1" {
		return fmt.Errorf("bootstrap contract must be change-request/v1")
	}
	if bootstrap.ArtifactName != "change-request" {
		return fmt.Errorf("bootstrap Artifact must be named change-request")
	}
	if _, err := bootstrapArtifactDigestFilename(bootstrap.ExpectedDigest); err != nil {
		return err
	}
	return nil
}

func loadBootstrapSource(ctx context.Context, c client.Client, workflow *v1alpha1.SovereignWorkflow) ([]byte, artifactcontract.ChangeRequest, error) {
	var source corev1.ConfigMap
	key := types.NamespacedName{Namespace: workflow.Namespace, Name: workflow.Spec.Bootstrap.SourceRef.Name}
	if err := c.Get(ctx, key, &source); err != nil {
		return nil, artifactcontract.ChangeRequest{}, fmt.Errorf("read bootstrap ConfigMap: %w", err)
	}
	if source.UID != workflow.Spec.Bootstrap.SourceRef.UID {
		return nil, artifactcontract.ChangeRequest{}, fmt.Errorf("bootstrap ConfigMap UID changed")
	}
	if source.Immutable == nil || !*source.Immutable {
		return nil, artifactcontract.ChangeRequest{}, fmt.Errorf("bootstrap ConfigMap must be immutable")
	}
	content, ok := source.BinaryData[workflow.Spec.Bootstrap.Key]
	if !ok {
		return nil, artifactcontract.ChangeRequest{}, fmt.Errorf("bootstrap ConfigMap is missing key %q", workflow.Spec.Bootstrap.Key)
	}
	if actual := artifactcontract.DigestBytes(content); actual != workflow.Spec.Bootstrap.ExpectedDigest {
		return nil, artifactcontract.ChangeRequest{}, fmt.Errorf("bootstrap ConfigMap digest mismatch: expected %s, got %s", workflow.Spec.Bootstrap.ExpectedDigest, actual)
	}
	if err := artifactcontract.DefaultRegistry().Validate(
		workflow.Spec.Bootstrap.Contract.Name, workflow.Spec.Bootstrap.Contract.Version, content,
	); err != nil {
		return nil, artifactcontract.ChangeRequest{}, fmt.Errorf("validate bootstrap ConfigMap: %w", err)
	}
	var changeRequest artifactcontract.ChangeRequest
	if err := json.Unmarshal(content, &changeRequest); err != nil {
		return nil, artifactcontract.ChangeRequest{}, fmt.Errorf("decode validated change request: %w", err)
	}
	return append([]byte(nil), content...), changeRequest, nil
}

func buildBootstrapJob(workflow *v1alpha1.SovereignWorkflow, image string, grant workspaceWriterGrant) *batchv1.Job {
	backoff := int32(0)
	activeDeadline := int64(120)
	automount := false
	allowPrivilegeEscalation := false
	readOnlyRoot := true
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        bootstrapJobName(workflow.Name),
			Namespace:   workflow.Namespace,
			Labels:      map[string]string{LabelWorkflow: workflow.Spec.WorkflowID},
			Annotations: workspaceWriterAnnotations(grant),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoff,
			ActiveDeadlineSeconds: &activeDeadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{LabelWorkflow: workflow.Spec.WorkflowID},
					Annotations: workspaceWriterAnnotations(grant),
				},
				Spec: corev1.PodSpec{
					RestartPolicy:                 corev1.RestartPolicyNever,
					AutomountServiceAccountToken:  &automount,
					TerminationGracePeriodSeconds: int64Pointer(10),
					Containers: []corev1.Container{{
						Name:  "bootstrap",
						Image: image,
						Args: []string{
							"--source", bootstrapMountRoot + "/" + workflow.Spec.Bootstrap.Key,
							"--artifact-store", bootstrapArtifactRoot,
							"--contract", workflow.Spec.Bootstrap.Contract.Name + "/" + workflow.Spec.Bootstrap.Contract.Version,
							"--expected-digest", workflow.Spec.Bootstrap.ExpectedDigest,
						},
						Env: workspaceWriterEnv(grant),
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: &allowPrivilegeEscalation,
							ReadOnlyRootFilesystem:   &readOnlyRoot,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "bootstrap-input", MountPath: bootstrapMountRoot, ReadOnly: true},
							{Name: "workspace", MountPath: "/workspace"},
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name: "bootstrap-input",
							VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: workflow.Spec.Bootstrap.SourceRef.Name},
							}},
						},
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: workflow.Status.PvcName,
							}},
						},
					},
				},
			},
		},
	}
}

func (r *WorkflowReconciler) releaseBootstrapWriter(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) error {
	if workflow.Status.BootstrapWriterEpoch < 1 || workflow.Status.BootstrapWriterReleased {
		return nil
	}
	holder, err := workspaceWriterIdentity("WorkflowBootstrap", workflow)
	if err != nil {
		return err
	}
	grant := workspaceWriterGrant{
		LeaseName: workflow.Status.WorkspaceWriterLeaseRef, HolderIdentity: holder, Epoch: workflow.Status.BootstrapWriterEpoch,
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	if err := releaseWorkspaceWriter(ctx, r.Client, workflow.Namespace, grant, now); err != nil {
		return err
	}
	updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
		latest.Status.BootstrapWriterReleased = true
	})
	if err != nil {
		return err
	}
	workflow.Status = updated.Status
	return nil
}

func (r *WorkflowReconciler) completeBootstrap(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, artifact *v1alpha1.Artifact) error {
	alreadyReady := apiMeta.IsStatusConditionTrue(workflow.Status.Conditions, "BootstrapReady")
	updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
		latest.Status.BootstrapArtifactRef = &v1alpha1.UIDReference{Name: artifact.Name, UID: artifact.UID}
		latest.Status.ObservedGeneration = latest.Generation
		apiMeta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: "BootstrapReady", Status: metav1.ConditionTrue, Reason: "ChangeRequestAccepted",
			Message:            "the exact submitted change request is stored and accepted",
			ObservedGeneration: latest.Generation, LastTransitionTime: metav1.Now(),
		})
	})
	if err != nil {
		return err
	}
	workflow.Status = updated.Status
	if !alreadyReady {
		if err := r.appendWorkflowEvent(ctx, updated, "WorkflowAdmitted", "", 0, "accept", artifact.Name, "accepted", "",
			map[string]string{"artifact": artifact.Name, "artifactUID": string(artifact.UID), "digest": artifact.Spec.Digest}, nil); err != nil {
			return err
		}
	}
	return r.cleanupBootstrapResources(ctx, updated)
}

func (r *WorkflowReconciler) acceptedBootstrapArtifact(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) (*v1alpha1.Artifact, error) {
	if workflow.Status.BootstrapArtifactRef == nil {
		return nil, fmt.Errorf("BootstrapReady has no Artifact reference")
	}
	var artifact v1alpha1.Artifact
	key := types.NamespacedName{Namespace: workflow.Namespace, Name: workflow.Status.BootstrapArtifactRef.Name}
	if err := r.Get(ctx, key, &artifact); err != nil {
		return nil, err
	}
	if artifact.UID != workflow.Status.BootstrapArtifactRef.UID {
		return nil, fmt.Errorf("bootstrap Artifact UID changed")
	}
	if err := validateBootstrapArtifactIdentity(workflow, &artifact); err != nil {
		return nil, err
	}
	if !bootstrapArtifactAccepted(&artifact) {
		return nil, fmt.Errorf("bootstrap Artifact is not accepted")
	}
	return &artifact, nil
}

func bootstrapArtifactAccepted(artifact *v1alpha1.Artifact) bool {
	if artifact.Status.Phase != v1alpha1.PhaseSucceeded ||
		artifact.Status.ObservedGeneration != artifact.Generation {
		return false
	}
	condition := apiMeta.FindStatusCondition(artifact.Status.Conditions, "Valid")
	return condition != nil &&
		condition.Status == metav1.ConditionTrue &&
		condition.ObservedGeneration == artifact.Generation
}

func validateBootstrapArtifactIdentity(workflow *v1alpha1.SovereignWorkflow, artifact *v1alpha1.Artifact) error {
	if !metav1.IsControlledBy(artifact, workflow) {
		return fmt.Errorf("bootstrap Artifact is not controlled by workflow")
	}
	workflowRef := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.ObjectMeta.UID}
	if artifact.Name != workflow.Spec.Bootstrap.ArtifactName ||
		artifact.Spec.WorkflowRef != workflowRef ||
		artifact.Spec.Contract != workflow.Spec.Bootstrap.Contract ||
		artifact.Spec.Digest != workflow.Spec.Bootstrap.ExpectedDigest ||
		artifact.Spec.Path != bootstrapArtifactPath(workflow.Spec.Bootstrap.ExpectedDigest) {
		return fmt.Errorf("bootstrap Artifact identity does not match admitted input")
	}
	if artifact.Spec.ProducerRef.APIVersion != v1alpha1.GroupVersion.String() ||
		artifact.Spec.ProducerRef.Kind != "SovereignWorkflow" ||
		artifact.Spec.ProducerRef.Name != workflow.Name {
		return fmt.Errorf("bootstrap Artifact producer is not workflow admission")
	}
	return nil
}

func (r *WorkflowReconciler) cleanupBootstrapResources(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) error {
	var source corev1.ConfigMap
	sourceKey := types.NamespacedName{Namespace: workflow.Namespace, Name: workflow.Spec.Bootstrap.SourceRef.Name}
	if err := r.Get(ctx, sourceKey, &source); err == nil {
		if source.UID != workflow.Spec.Bootstrap.SourceRef.UID {
			return fmt.Errorf("refusing to delete recreated bootstrap ConfigMap")
		}
		if err := r.Delete(ctx, &source); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	var job batchv1.Job
	jobKey := types.NamespacedName{Namespace: workflow.Namespace, Name: bootstrapJobName(workflow.Name)}
	if err := r.Get(ctx, jobKey, &job); err == nil {
		if !metav1.IsControlledBy(&job, workflow) {
			return fmt.Errorf("refusing to delete bootstrap Job not controlled by workflow")
		}
		if err := r.Delete(ctx, &job); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func artifactConditionMessage(artifact *v1alpha1.Artifact) string {
	condition := apiMeta.FindStatusCondition(artifact.Status.Conditions, "Valid")
	if condition == nil || condition.Message == "" {
		return "bootstrap Artifact was rejected"
	}
	return condition.Message
}

func bootstrapJobName(workflowName string) string {
	const suffix = "-bootstrap"
	maximumPrefix := 63 - len(suffix)
	prefix := strings.Trim(workflowName, "-")
	if len(prefix) > maximumPrefix {
		prefix = strings.TrimRight(prefix[:maximumPrefix], "-")
	}
	return prefix + suffix
}

func bootstrapArtifactPath(digest string) string {
	filename, _ := bootstrapArtifactDigestFilename(digest)
	return bootstrapArtifactRoot + "/" + filename
}

func bootstrapArtifactDigestFilename(digest string) (string, error) {
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("bootstrap digest must use sha256")
	}
	filename := strings.TrimPrefix(digest, "sha256:")
	if len(filename) != 64 {
		return "", fmt.Errorf("bootstrap digest must contain 64 hexadecimal characters")
	}
	for _, character := range filename {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return "", fmt.Errorf("bootstrap digest must contain lowercase hexadecimal characters")
			}
		}
	}
	return filename, nil
}

func int64Pointer(value int64) *int64 { return &value }
