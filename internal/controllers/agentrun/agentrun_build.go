package agentrun

import (
	"encoding/json"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/controllers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func BuildAgentRunPod(run *v1alpha1.AgentRun, pvcName, configName, mcpImage string, grant controllers.WorkspaceWriterGrant) *corev1.Pod {
	if pvcName == "" {
		pvcName = run.Spec.WorkflowRef.Name + "-workspace"
	}
	automount, readOnly, allowPrivilegeEscalation := false, true, false
	sidecarRestartPolicy := corev1.ContainerRestartPolicyAlways
	executable, _ := json.Marshal(run.Spec.Executable)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      run.Name,
			Namespace: run.Namespace,
			Labels: map[string]string{
				controllermeta.LabelWorkflow: run.Labels[controllermeta.LabelWorkflow],
				controllermeta.LabelStep:     run.Spec.StepName,
				"sovereign-ai.io/agent-run":  run.Name,
			},
			Annotations: controllers.WorkspaceWriterAnnotations(grant)},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &automount,
			SecurityContext: controllers.WorkspaceWorkloadSecurityContext(),
			InitContainers: []corev1.Container{{
				Name:          "mcp",
				Image:         mcpImage,
				RestartPolicy: &sidecarRestartPolicy,
				Env: []corev1.EnvVar{
					{Name: "SOVEREIGN_MCP_ADDRESS", Value: "127.0.0.1:8080"},
					{Name: "SOVEREIGN_MCP_REQUIRE_IDENTITY", Value: "false"},
					{Name: "SOVEREIGN_AGENT_INPUT", Value: "/control/input.json"},
					{Name: "SOVEREIGN_REPOSITORY_ROOT", Value: "/repository"},
					{Name: "SOVEREIGN_WORKSPACE_OVERLAY_ROOT", Value: controllers.ExecutionStagingPath(run.Name) + "/overlay"},
				},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowPrivilegeEscalation, ReadOnlyRootFilesystem: &readOnly, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "workspace", MountPath: "/repository", ReadOnly: true},
					{Name: "workspace", MountPath: "/workspace"},
					{Name: "input", MountPath: "/control", ReadOnly: true},
				},
			}},
			Containers: []corev1.Container{
				{
					Name:            "agent",
					Image:           run.Spec.Image,
					Command:         []string{"/agent-wrapper"},
					Args:            []string{"--input", "/control/input.json", "--result", controllers.ExecutionResultPath(run.Name)},
					Env:             append([]corev1.EnvVar{{Name: "SOVEREIGN_AGENT_EXECUTABLE", Value: string(executable)}}, controllers.WorkspaceWriterEnv(grant)...),
					SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowPrivilegeEscalation, ReadOnlyRootFilesystem: &readOnly, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
					VolumeMounts:    []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "input", MountPath: "/control", ReadOnly: true}},
				}}, Volumes: []corev1.Volume{
				{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName}}},
				{Name: "input", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}},
			},
		}}
}
