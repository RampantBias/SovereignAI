package kube

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Swap to Job
func buildCommitterPod(workflowID string, gitBranch string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("git-committer-%s", workflowID),
		},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:  "git-bot",
					Image: "alpine/git:latest",
					// The script executes the push directly from the shared PVC
					Command: []string{"/bin/sh", "-c"},
					Args: []string{fmt.Sprintf(`
                        cd /workspace
                        git config user.name "Project AIM Bot"
                        git config user.email "bot@sovereign-forge.local"
                        git checkout -b %s
                        git add .
                        git commit -m "Automated commit: QA Passed for Workflow %s"
                        # Securely use the injected token in the remote URL
                        git push https://${GIT_USER}:${GIT_TOKEN}@github.com/org/app-repo.git %s
                        
                        # Write the success contract for the Orchestrator
                        echo '{"status": "completed"}' > /workspace/status.json
                    `, gitBranch, workflowID, gitBranch)},
					// Securely inject the token from the K8s Secret
					Env: []v1.EnvVar{
						{
							Name: "GIT_USER",
							ValueFrom: &v1.EnvVarSource{
								SecretKeyRef: &v1.SecretKeySelector{
									LocalObjectReference: v1.LocalObjectReference{Name: "project-alpha-git-credentials"},
									Key:                  "GIT_USER",
								},
							},
						},
						{
							Name: "GIT_TOKEN",
							ValueFrom: &v1.EnvVarSource{
								SecretKeyRef: &v1.SecretKeySelector{
									LocalObjectReference: v1.LocalObjectReference{Name: "project-alpha-git-credentials"},
									Key:                  "GIT_TOKEN",
								},
							},
						},
					},
					VolumeMounts: []v1.VolumeMount{
						{Name: "workspace-pvc", MountPath: "/workspace"},
					},
				},
			},
			Volumes: []v1.Volume{
				// ... your standard PVC mount here ...
			},
		},
	}
}
