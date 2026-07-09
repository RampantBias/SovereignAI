package kube

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/SovereignAI/internal/orchestration"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// KubeManager ctor
func NewKubeManager(clientset kubernetes.Interface) *KubeManager {
	return &KubeManager{clientset: clientset}
}

// TODO: Wrap shared resource labels into sharedLabels
func getSharedLabels(namespace string, workflowID string) map[string]string {
	return map[string]string{
		"sovereign-orchestrator-task":        namespace,
		"sovereign-orchestrator/workflow-id": workflowID,
		"app.kubernetes.io/managed-by":       "sovereign-orchestrator",
	}
}

func (k *KubeManager) GetNodes(ctx context.Context) ([]orchestration.NodeSpec, error) {
	knodes, err := k.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var nodes []orchestration.NodeSpec
	for _, node := range knodes.Items {
		// Evaluate Node's Ready status
		isReady := false
		for _, cond := range node.Status.Conditions {
			if cond.Type == v1.NodeReady {
				isReady = (cond.Status == v1.ConditionTrue)
			}
		}

		nodeSpec := orchestration.NodeSpec{
			IsReady: isReady,
			Name:    node.Name,
		}

		// Get GPU specs (Assume written by Nvidia Operator and bootstrapper)
		model := node.Labels["nvidia.com/gpu.product"]
		gpuCount, err := strconv.Atoi(node.Labels["nvidia.com/gpu.count"])
		if err != nil {
			// TODO
		}
		vram, err := strconv.Atoi(node.Labels["nvidia.com/gpu.mem.each"]) //per-gpu
		if err != nil {
			log.Printf("failed to find GPU memory spec in node label for %s", node.Name)
			// TODO: Resolve cleanly
			vram = 0
		}

		// Add GPU info
		nodeSpec.GPUs = make([]orchestration.GPUSpec, gpuCount)
		for i := range gpuCount {
			nodeSpec.GPUs[i] = orchestration.GPUSpec{
				Index:     i,
				Model:     model,
				TotalVRAM: int64(vram),
			}
		}

		// append node
		nodes = append(nodes, nodeSpec)
	}
	return nodes, nil
}

// /***************************
// / Health Check
// /***************************
func (k *KubeManager) CheckKubernetesHealth(ctx context.Context) orchestration.ComponentStatus {
	status := orchestration.ComponentStatus{Name: "Kubernetes API"}

	start := time.Now()
	// Using ServerVersion as a lightweight, authenticated ping
	_, err := k.clientset.Discovery().ServerVersion()
	status.Latency = time.Since(start)

	if err != nil {
		status.Online = false
		status.Error = fmt.Errorf("failed to reach API server: %w", err)
		return status
	}

	status.Online = true
	return status
}

// / Reviews RBAC setup on the cluster and ensures proper permissions
// Check if account/role/rb exists -> Create if it doesn't
func (k *KubeManager) BootstrapRBAC(ctx context.Context) error {

	//saList, err := k.clientset.CoreV1().ServiceAccounts("sovereign-ai").List(ctx, metav1.ListOptions{})
	return nil
}
