package orchestration

import (
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func getSharedLabels(namespace string, workflowID string) map[string]string {
	return map[string]string{
		"sovereign-ai.io/workflow-namespace": namespace,
		"sovereign-ai.io/workflow-id":        workflowID,
		"app.kubernetes.io/managed-by":       "sovereign-orchestrator",
	}
}

func buildNamespace(sw *v1alpha1.SovereignWorkflow) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: sw.Namespace,
			Labels: map[string]string{
				"sovereign-orchestrator/workflow-id": sw.Spec.WorkflowID,
				"app.kubernetes.io/managed-by":       "sovereign-orchestrator",
				"sovereign-orchestrator/project":     sw.Spec.ProjectName,
			},
		},
	}
}

func (c *SovereignWorkflowController) buildAgentPod(sw *v1alpha1.SovereignWorkflow, stepName string, placement PlacementResult) (*corev1.Pod, error) {
	// Build labels & annotations
	labelMap := getSharedLabels(sw.Namespace, sw.Spec.WorkflowID)
	labelMap["sovereign-ai.io/step-name"] = stepName

	annotations := map[string]string{
		"sovereign-ai.io/assigned-gpu-id": fmt.Sprint(placement.GPUIndex),
	}

	runtimeClass := "nvidia"
	stepConfig := GetStepConfig(sw, stepName)

	// Set architects
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-%s", sw.Spec.WorkflowID, stepName),
			Labels:      labelMap,
			Namespace:   sw.Namespace,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			RestartPolicy:    corev1.RestartPolicyNever, // Agents perform a task and exit
			NodeName:         placement.NodeName,
			RuntimeClassName: &runtimeClass,
			Containers: []corev1.Container{
				{
					Name:  "agent",
					Image: stepConfig.Image,
					//ImagePullPolicy: v1.PullPolicy(),
					Env: []corev1.EnvVar{
						// GPU/vLLM Endpoint
						corev1.EnvVar{
							Name:  "VLLM_ENDPOINT",
							Value: fmt.Sprint(placement.GPUIndex), //placeholder
						},
						// Workflow ID to pass to other APIs
						corev1.EnvVar{
							Name:  "SOVEREIGN_WORKFLOW_ID",
							Value: sw.Spec.WorkflowID,
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "workspace",
							MountPath: "/workspace",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "workspace",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: fmt.Sprintf("%s-workspace", sw.Spec.WorkflowID),
						},
					},
				},
			},
			// TODO: Need to add service account permissions
		},
	}

	// Bind pod to workflow lifecycle
	if err := controllerutil.SetControllerReference(sw, pod, c.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference on pod: %w", err)
	}

	return pod, nil
}

func (c *SovereignWorkflowController) buildHumanPod(sw *v1alpha1.SovereignWorkflow, stepName string) (*corev1.Pod, error) {
	// Build labels & annotations
	labelMap := getSharedLabels(sw.Namespace, sw.Spec.WorkflowID)
	labelMap["sovereign-ai.io/step-name"] = stepName

	stepConfig := GetStepConfig(sw, stepName)

	// Set architects
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", sw.Spec.WorkflowID, stepName),
			Labels:    labelMap,
			Namespace: sw.Namespace,
			//Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, // Agents perform a task and exit
			Containers: []corev1.Container{
				{
					Name:  "agent",
					Image: stepConfig.Image,
					//ImagePullPolicy: v1.PullPolicy(),
					Env: []corev1.EnvVar{
						// Endpoint for primary agent
						// corev1.EnvVar{
						// 	Name:  "AGENT_ENDPOINT",
						// 	Value: fmt.Sprint(placement.GPUIndex), //placeholder
						// },
						// Workflow ID to pass to other APIs
						corev1.EnvVar{
							Name:  "SOVEREIGN_WORKFLOW_ID",
							Value: sw.Spec.WorkflowID,
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "workspace",
							MountPath: "/workspace",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "workspace",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: fmt.Sprintf("%s-workspace", sw.Spec.WorkflowID),
						},
					},
				},
			},
			// TODO: Need to add service account permissions
		},
	}

	// Bind pod to workflow lifecycle
	if err := controllerutil.SetControllerReference(sw, pod, c.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference on pod: %w", err)
	}

	return pod, nil
}

func buildNetworkPolicy(name string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deny-cross-namespace-traffic",
			Namespace: name,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			// Only allow ingress to pods within namespace
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{},
						},
					},
				},
			},
			// Only allow egress to pods within namespace
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{},
						},
					},
				},
			},
		},
	}
}

func buildPVC(config PvcConfig) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.Name,
			Labels:    getSharedLabels(config.Namespace, config.WorkflowID),
			Namespace: config.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &config.StorageClassName, // This is your "future-proofing"
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(config.StorageSize),
				},
			},
		},
	}

	return pvc
}

// Build vLLM pod
func buildVLLMPod(name, namespace, modelName string, placement PlacementResult) *corev1.Pod {
	nodeName := placement.NodeName

	// Standardize the vLLM labels so the Headless Service can find it
	labels := make(map[string]string)
	labels["app.kubernetes.io/name"] = "vllm-server"
	labels["sovereign-ai.io/model"] = modelName
	labels["sovereign-ai.io/gpu-index"] = placement.GPUIndex
	labels["sovereign-ai.io/endpoint-id"] = name

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Labels:     labels,
			Finalizers: []string{"sovereign-orchestrator.io/vllm-cleanup"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways, // It's a server, keep it alive
			NodeName:      nodeName,
			Containers: []corev1.Container{
				{
					Name:  "vllm",
					Image: "vllm/vllm-openai:latest", //TODO: Route via configuration
					Command: []string{
						"python3", "-m", "vllm.entrypoints.openai.api_server",
					},
					Args: []string{
						"--model", modelName,
						"--port", "8000",
						"--gpu-memory-utilization", "0.90",
					},
					Ports: []corev1.ContainerPort{
						{ContainerPort: 8000, Name: "http"},
					},
					// Crucial: Request the physical GPU slice
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							"nvidia.com/gpu": resource.MustParse("1"),
						},
					},
					Env: []corev1.EnvVar{
						{
							Name:  "CUDA_VISIBLE_DEVICES",
							Value: placement.GPUIndex,
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "model-cache",
							MountPath: "/root/.cache/huggingface", // Standard cache path
						},
						{
							Name:      "dshm",
							MountPath: "/dev/shm", // Required for vLLM/PyTorch tensor operations
						},
					},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/health",
								Port: intstr.FromString("http"),
							},
						},
						InitialDelaySeconds: 120, //Get load time estimate based on model size
						PeriodSeconds:       15,
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/health",
								Port: intstr.FromString("http"),
							},
						},
						PeriodSeconds: 5,
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					// Mounts the physical node's cache so weights aren't re-downloaded
					Name: "model-cache",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/mnt/local/ai-models", // Or wherever your server stores weights
						},
					},
				},
				{
					// Fixes "Bus error" in PyTorch by expanding shared memory
					Name: "dshm",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium: corev1.StorageMediumMemory,
						},
					},
				},
			},
		},
	}
}

func buildVLLMService(namespace, modelName, name string) *corev1.Service {
	// Standardize labels to match the specific pod EXACTLY
	selectorLabels := map[string]string{
		"app.kubernetes.io/name":      "vllm-server",
		"sovereign-ai.io/model":       modelName,
		"sovereign-ai.io/endpoint-id": name,
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Labels:     selectorLabels,
			Finalizers: []string{"sovereign-orchestrator.io/vllm-cleanup"},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None", // Headless Service
			Selector:  selectorLabels,
			Ports: []corev1.ServicePort{
				{
					Name: "http-inference",
					Port: 8000, // vLLM default
				},
			},
		},
	}
	return svc
}
