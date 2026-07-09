//go:build nvidia

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

type GPUMemory struct {
	Total int64
	Used  int64
}

var (
	scheme = runtime.NewScheme()
)

func init() {
	_ = v1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Fatal("NODE_NAME environment variable is required")
	}

	// Grab k8s client
	restConfig := ctrl.GetConfigOrDie()
	k8sClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("unable to create client: %v", err)
	}

	// Initialize NVML
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		log.Fatalf("Failed to initialize NVML: %v", nvml.ErrorString(ret))
	}

	// Guarantee C-library memory is freed on graceful exit
	defer func() {
		log.Println("Shutting down NVML...")
		nvml.Shutdown()
	}()

	// Acquire Device Handle
	device, ret := nvml.DeviceGetHandleByIndex(0)
	if ret != nvml.SUCCESS {
		log.Fatalf("Failed to get device handle: %v", nvml.ErrorString(ret))
	}

	// Ensure CRDs are bootstrapped and owned by node
	boostrapCRDs(ctx, k8sClient, nodeName, device)

	// Perform metrics loop
	reconcileMetrics(ctx, k8sClient, nodeName, device)
}

// Create GPU Node CRD containing GPU information
func boostrapCRDs(ctx context.Context, k8sClient client.Client, nodeName string, device nvml.Device) {
	var k8sNode corev1.Node
	k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &k8sNode)

	// Check if GPU Node CRD already exists
	var existingGpuCrd v1alpha1.GPUNode
	err := k8sClient.Get(ctx, types.NamespacedName{Name: k8sNode.Name}, &existingGpuCrd)
	if err == nil {
		return // Already exists
	}

	// Get total vram
	vram, err := queryNVMLForVRAM(device)
	if err != nil {
		log.Fatalf("Cannot write GPU CRD for node %s due to NVML failure: %w", &k8sNode.Name, err)
	}

	// Generate manifest for GPU CRD
	gpuNodeCRD := &v1alpha1.GPUNode{
		ObjectMeta: metav1.ObjectMeta{
			Name: k8sNode.Name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "Node",
					Name:       k8sNode.Name,
					UID:        k8sNode.UID,
				},
			},
		},
		Spec: v1alpha1.GPUSpec{
			NodeName:  k8sNode.Name,
			TotalVRAM: vram.Total,
		},
	}

	// Create CRD
	k8sClient.Create(ctx, gpuNodeCRD)
}

func reconcileMetrics(ctx context.Context, client client.Client, nodeName string, device nvml.Device) error {
	//
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Context was cancelled
			return ctx.Err()
		case <-ticker.C:
			interrogate(ctx, client, nodeName, device)
		}
	}
}

func interrogate(ctx context.Context, k8sClient client.Client, nodeName string, device nvml.Device) {
	vram, err := queryNVMLForVRAM(device)
	if err != nil {
		log.Printf("Failed to query NVML: %v", err)
		return
	}

	// Fetch GPU CRD and update if necessary
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var gpuNode v1alpha1.GPUNode
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &gpuNode); err != nil {
			return err
		}

		// Prevent API spam: Only update if the physical memory changed
		if gpuNode.Status.UsedVRAM == vram.Used {
			return nil
		}

		gpuNode.Status.UsedVRAM = vram.Used

		// Apply the update. If the Orchestrator updated AllocatedVRAM at the
		// exact same time, this returns a Conflict, and the retry block will
		// automatically re-fetch the fresh CRD and run this function again.
		return k8sClient.Status().Update(ctx, &gpuNode)
	})

	if err != nil {
		log.Printf("Failed to sync physical VRAM metrics to CRD: %v", err)
	}
}

// queryNVMLForUsedVRAM isolates the CGO/NVML bindings
func queryNVMLForVRAM(device nvml.Device) (GPUMemory, error) {
	// NVML bindings wrap C code. It must be explicitly initialized.
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		return GPUMemory{}, fmt.Errorf("failed to initialize NVML: %v", nvml.ErrorString(ret))
	}
	// Guarantee cleanup of the C library state
	defer nvml.Shutdown()

	// This maps directly to the nvmlDeviceGetMemoryInfo C function
	memory, ret := device.GetMemoryInfo()
	if ret != nvml.SUCCESS {
		return GPUMemory{}, fmt.Errorf("failed to get memory info: %v", nvml.ErrorString(ret))
	}

	return GPUMemory{
		Total: int64(memory.Total),
		Used:  int64(memory.Used),
	}, nil
}
