package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/controllermeta"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	bootstrapArtifactRoot = "/workspace/.sovereign/artifacts"
	bootstrapMountRoot    = "/bootstrap"
)

func ValidateBootstrapArtifactIdentity(workflow *v1alpha1.SovereignWorkflow, artifact *v1alpha1.Artifact) error {
	if !metav1.IsControlledBy(artifact, workflow) {
		return fmt.Errorf("bootstrap Artifact is not controlled by workflow")
	}
	workflowRef := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.ObjectMeta.UID}
	if artifact.Name != workflow.Spec.Bootstrap.ArtifactName ||
		artifact.Spec.WorkflowRef != workflowRef ||
		artifact.Spec.Contract != workflow.Spec.Bootstrap.Contract ||
		artifact.Spec.Digest != workflow.Spec.Bootstrap.ExpectedDigest ||
		artifact.Spec.Path != BootstrapArtifactPath(workflow.Spec.Bootstrap.ExpectedDigest) {
		return fmt.Errorf("bootstrap Artifact identity does not match admitted input")
	}
	if artifact.Spec.ProducerRef.APIVersion != v1alpha1.GroupVersion.String() ||
		artifact.Spec.ProducerRef.Kind != "SovereignWorkflow" ||
		artifact.Spec.ProducerRef.Name != workflow.Name {
		return fmt.Errorf("bootstrap Artifact producer is not workflow admission")
	}
	return nil
}

func LoadBootstrapChangeRequest(ctx context.Context, c client.Client, workflow *v1alpha1.SovereignWorkflow) ([]byte, artifactcontract.ChangeRequest, error) {
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

func BootstrapArtifactPath(digest string) string {
	filename, _ := BootstrapArtifactDigestFilename(digest)
	return bootstrapArtifactRoot + "/" + filename
}

func BootstrapArtifactDigestFilename(digest string) (string, error) {
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

func BootstrapJobName(workflowName string) string {
	const suffix = "-bootstrap"
	maximumPrefix := 63 - len(suffix)
	prefix := strings.Trim(workflowName, "-")
	if len(prefix) > maximumPrefix {
		prefix = strings.TrimRight(prefix[:maximumPrefix], "-")
	}
	return prefix + suffix
}

func BuildBootstrapJob(workflow *v1alpha1.SovereignWorkflow, image string, grant WorkspaceWriterGrant) *batchv1.Job {
	backoff := int32(0)
	activeDeadline := int64(120)
	automount := false
	allowPrivilegeEscalation := false
	readOnlyRoot := true
	terminationPeriod := int64(10)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        BootstrapJobName(workflow.Name),
			Namespace:   workflow.Namespace,
			Labels:      map[string]string{controllermeta.LabelWorkflow: workflow.Spec.WorkflowID},
			Annotations: WorkspaceWriterAnnotations(grant),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoff,
			ActiveDeadlineSeconds: &activeDeadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{controllermeta.LabelWorkflow: workflow.Spec.WorkflowID},
					Annotations: WorkspaceWriterAnnotations(grant),
				},
				Spec: corev1.PodSpec{
					RestartPolicy:                 corev1.RestartPolicyNever,
					AutomountServiceAccountToken:  &automount,
					TerminationGracePeriodSeconds: &terminationPeriod,
					SecurityContext:               WorkspaceWorkloadSecurityContext(),
					Containers: []corev1.Container{{
						Name:  "bootstrap",
						Image: image,
						Args: []string{
							"--source", bootstrapMountRoot + "/" + workflow.Spec.Bootstrap.Key,
							"--artifact-store", bootstrapArtifactRoot,
							"--contract", workflow.Spec.Bootstrap.Contract.Name + "/" + workflow.Spec.Bootstrap.Contract.Version,
							"--expected-digest", workflow.Spec.Bootstrap.ExpectedDigest,
						},
						Env: WorkspaceWriterEnv(grant),
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
