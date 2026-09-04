package controllers

import (
	"os"

	"github.com/SovereignAI/internal/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func BuildCollectorResources(owner client.Object, workflow *v1alpha1.SovereignWorkflow, stepName, image string, grant WorkspaceWriterGrant) []client.Object {
	name := owner.GetName() + "-collector"
	jobName := owner.GetName() + "-collect"
	automount, backoff := true, int32(0)
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: owner.GetNamespace()}, AutomountServiceAccountToken: &automount}
	producerGrantName, producerGrantUID := "", ""
	if controller := metav1.GetControllerOf(owner); controller != nil && controller.Kind == "StepAttempt" {
		producerGrantName, producerGrantUID = controller.Name, string(controller.UID)
	}
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: owner.GetNamespace(),
		},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"artifacts"}, Verbs: []string{"create", "get"}}}}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: owner.GetNamespace(),
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: name, Namespace: owner.GetNamespace()},
		},
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        jobName,
			Namespace:   owner.GetNamespace(),
			Annotations: WorkspaceWriterAnnotations(grant)},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: WorkspaceWriterAnnotations(grant),
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever, ServiceAccountName: name,
					SecurityContext: WorkspaceWorkloadSecurityContext(),
					Containers: []corev1.Container{{
						Name:  "collector",
						Image: image,
						Args: []string{
							"--namespace", owner.GetNamespace(),
							"--workflow", workflow.Name,
							"--workflowuid", string(workflow.ObjectMeta.UID),
							"--attempt", owner.GetName(),
							"--producer-kind", producerKind(owner),
							"--producer-api-version", v1alpha1.GroupVersion.String(),
							"--producer-uid", string(owner.GetUID()),
							"--producer-grant", producerGrantName,
							"--producer-grant-uid", producerGrantUID,
							"--result", ExecutionResultPath(owner.GetName()),
							"--staging", ExecutionStagingPath(owner.GetName()),
							"--artifact-store", "/workspace/.sovereign/artifacts",
							"--audit-events", ExecutionAuditEventsPath(owner.GetName()),
							"--source-revision", workflow.Spec.DefinitionRevision,
						},
						Env: append(collectorAuditEnv(), WorkspaceWriterEnv(grant)...),
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "workspace",
							MountPath: "/workspace"}}},
					},
					Volumes: []corev1.Volume{{
						Name: "workspace",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: workflow.Status.PvcName,
							},
						},
					},
					},
				},
			},
		},
	}
	_ = stepName
	return []client.Object{serviceAccount, role, binding, job}
}

func collectorAuditEnv() []corev1.EnvVar {
	var env []corev1.EnvVar
	for _, name := range []string{"SOVEREIGN_AUDIT_DSN", "SOVEREIGN_AUDIT_REQUIRED"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, corev1.EnvVar{Name: name, Value: value})
		}
	}
	return env
}

func producerKind(owner client.Object) string {
	switch owner.(type) {
	case *v1alpha1.AgentRun:
		return "AgentRun"
	case *v1alpha1.UtilityOperation:
		return "UtilityOperation"
	default:
		return "Unknown"
	}
}
