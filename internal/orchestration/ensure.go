package orchestration

import (
	"context"
	"errors"
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var (
	ErrNoCapacity       = errors.New("no GPU nodes have sufficient available VRAM")
	ErrConflict         = errors.New("optimistic lock conflict during GPU allocation")
	ErrEndpointVanished = errors.New("endpoint vanished during GPU allocation")
)

func (c *SovereignWorkflowController) ensureStorage(ctx context.Context, sw *v1alpha1.SovereignWorkflow, stepName string) error {
	// Check if PVC is already active
	pvcName := fmt.Sprintf("%s-workspace", sw.Name)

	var existingPvc corev1.PersistentVolumeClaim
	err := c.Client.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: sw.Namespace}, &existingPvc)

	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to inspect cluster for existing PVC: %w", err)
	}

	// Generate Pvc manifest
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Labels:    getSharedLabels(sw.Namespace, sw.Spec.WorkflowID),
			Namespace: sw.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &c.Config.StorageClassName,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(sw.Spec.RequestedVolumeSize),
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(sw, pvc, c.Scheme); err != nil {
		return fmt.Errorf("failed to bind PVC owner reference: %w", err)
	}

	if err := c.Client.Create(ctx, pvc); err != nil {
		// avoid race condition for concurrent creation
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("failed to create workflow %s pvc for: %w", sw.Spec.WorkflowID, err)
	}
	return nil
}

func (c *SovereignWorkflowController) ensureInference(ctx context.Context, sw *v1alpha1.SovereignWorkflow) (PlacementResult, error) {
	// Inference Namespace Boundary
	if err := c.ensureInferenceNamespace(ctx); err != nil {
		return PlacementResult{}, err
	}

	// Get current step details
	activeStepName := sw.Status.ActiveStepName
	stepStatus := c.getStepStatus(sw, activeStepName)

	// Ensure idempotency by verifying whether the current step has already secured placement
	placement := c.ensureNoActiveInferenceEndpoint(ctx, sw.Spec.WorkflowID, stepStatus)
	if placement.Action == "Existing" {
		return placement, nil
	}

	// Get allocation request
	allocationRequest := c.getStepAllocationRequest(sw, activeStepName)

	// Find node to host inference (or existing)
	placement, err := c.GpuService.FindPlacement(ctx, allocationRequest)
	if err != nil {
		return PlacementResult{}, fmt.Errorf("GPU placement failed for workflow %s step %s: %w", sw.Spec.WorkflowID, stepStatus.StepName, err)
	}

	// Atomic Transaction Layer for updating GPU CRD (Optimistic Concurrency)
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latestNode := &v1alpha1.GPUNode{}
		if err := c.Client.Get(ctx, types.NamespacedName{Name: placement.NodeName}, latestNode); err != nil {
			return err
		}

		// Recompute available VRAM to verify capacity condition is still true
		effectiveUsed := max(latestNode.Status.AllocatedVRAM, latestNode.Status.UsedVRAM)
		availableVRAM := latestNode.Spec.TotalVRAM - effectiveUsed

		switch placement.Action {
		case "Create":
			if availableVRAM < (allocationRequest.RequiredKVVRAM + allocationRequest.RequiredModelVRAM) {
				return fmt.Errorf("VRAM capacity lost to parallel thread during reconciliation: %w", ErrNoCapacity)
			}

			latestNode.Status.AllocatedVRAM += allocationRequest.RequiredKVVRAM + allocationRequest.RequiredModelVRAM
			latestNode.Status.ActiveInferenceEndpoints = append(latestNode.Status.ActiveInferenceEndpoints, v1alpha1.LegacyInferenceEndpoint{
				ModelID:         allocationRequest.ModelName,
				GPUIndex:        placement.GPUIndex,
				EndpointURL:     placement.EndpointURL,
				AllocatedVRAM:   allocationRequest.RequiredKVVRAM + allocationRequest.RequiredModelVRAM,
				ActiveWorkflows: []string{sw.Spec.WorkflowID},
			})

		case "Reuse":
			if availableVRAM < allocationRequest.RequiredKVVRAM {
				return fmt.Errorf("VRAM capacity exhausted for cache reuse: %w", ErrNoCapacity)
			}

			found := false

			// Search for re-used inference endpoint and register Workflow and VRAM allocation
			for i := range latestNode.Status.ActiveInferenceEndpoints {
				if latestNode.Status.ActiveInferenceEndpoints[i].EndpointURL == placement.EndpointURL {
					latestNode.Status.ActiveInferenceEndpoints[i].ActiveWorkflows = append(
						latestNode.Status.ActiveInferenceEndpoints[i].ActiveWorkflows,
						sw.Spec.WorkflowID,
					)
					latestNode.Status.ActiveInferenceEndpoints[i].AllocatedVRAM += allocationRequest.RequiredKVVRAM
					latestNode.Status.AllocatedVRAM += allocationRequest.RequiredKVVRAM
					found = true
					break
				}
			}

			// Core Strategy: If an engine was deleted mid-flight, abort the transaction path
			if !found {
				return fmt.Errorf("reused endpoint vanished during commit cycle: %w", ErrEndpointVanished)
			}
		}

		return c.Client.Status().Update(ctx, latestNode)
	})

	if err != nil {
		return PlacementResult{}, fmt.Errorf("ledger commit failed: %w", err)
	}

	// Infrastructure plane provisioning (vLLM + service)
	if placement.Action == "Create" {
		if err := c.deployInferenceInfrastructure(ctx, placement, allocationRequest, sw, stepStatus); err != nil {
			return PlacementResult{}, err
		}
	}

	return placement, nil
}

func (c *SovereignWorkflowController) ensureInferenceNamespace(ctx context.Context) error {
	var ns corev1.Namespace
	if err := c.Client.Get(ctx, types.NamespacedName{Namespace: "sovereign-inference"}, &ns); err != nil {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "sovereign-inference",
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sovereign-orchestrator",
				},
			},
		}

		if err := c.Client.Create(ctx, ns); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				// failed for other reason
				return fmt.Errorf("failed to create inference namespace: %w", err)
			}
		}
	}
	return nil
}

func (c *SovereignWorkflowController) ensureNoActiveInferenceEndpoint(ctx context.Context, workflowID string, stepStatus v1alpha1.StepRuntimeStatus) PlacementResult {
	if stepStatus.InferenceEndpointURL != "" {
		// Query the node to verify our lease is still recorded in the ledger
		// (This protects against the node CRD being wiped or modified externally)
		var latestNode v1alpha1.GPUNode
		err := c.Client.Get(ctx, types.NamespacedName{Name: stepStatus.AssignedNode}, &latestNode)
		if err == nil {
			for _, endpoint := range latestNode.Status.ActiveInferenceEndpoints {
				if endpoint.EndpointURL == stepStatus.InferenceEndpointURL {
					// Double check that we are registered in the active slice
					for _, wfID := range endpoint.ActiveWorkflows {
						if wfID == workflowID {
							// Perfect state match. Skip ledger allocation and return existing placement
							return PlacementResult{
								NodeName:    stepStatus.AssignedNode,
								GPUIndex:    endpoint.GPUIndex,
								EndpointURL: endpoint.EndpointURL,
								Action:      "Existing", // Custom marker or handle as a non-op
							}
						}
					}
				}
			}
		}
	}
	return PlacementResult{}
}

func (c *SovereignWorkflowController) deployInferenceInfrastructure(ctx context.Context, placement PlacementResult, req AllocationRequest, sw *v1alpha1.SovereignWorkflow, stepStatus v1alpha1.StepRuntimeStatus) error {
	// Get name for pod/svc
	name := fmt.Sprintf("vllm-%s-%s-%s", placement.NodeName, sw.Spec.WorkflowID, placement.GPUIndex)
	endpointURL := fmt.Sprintf("%s.default.svc.cluster.local", name)

	// Schedule vLLM pod with model onto placement node
	vLLMPod := buildVLLMPod(name, sw.Namespace, req.ModelName, placement)
	if err := c.Client.Get(ctx, types.NamespacedName{Namespace: sw.Namespace, Name: stepStatus.PodName}, vLLMPod); err != nil {
		// Deploy vLLM Pod
		err := c.Client.Create(ctx, vLLMPod)
		if err != nil {
			return fmt.Errorf("failed to create vLLM Pod: %w", err)
		}

		// Generate headless service and fill in EndpointURL
		svc := buildVLLMService(sw.Namespace, req.ModelName, name)
		if err := c.Client.Create(ctx, svc); err != nil {
			return fmt.Errorf("failed to create the vLLM service: %w", err)
		}

	}
	placement.InferencePodName = name
	placement.EndpointURL = endpointURL
	return nil
}

func (c *SovereignWorkflowController) getStepStatus(sw *v1alpha1.SovereignWorkflow, activeStepName string) v1alpha1.StepRuntimeStatus {
	for _, status := range sw.Status.StepStatuses {
		if status.StepName == activeStepName {
			return status
		}
	}
	return v1alpha1.StepRuntimeStatus{}
}

func (c *SovereignWorkflowController) getStepAllocationRequest(sw *v1alpha1.SovereignWorkflow, stepName string) AllocationRequest {
	for _, step := range sw.Spec.Steps {
		if step.Name == stepName {
			return AllocationRequest{
				WorkflowID:     sw.Spec.WorkflowID,
				RequiredKVVRAM: step.RequestedVRAMAllocation,
				ModelName:      step.ModelName,
			}
		}
	}
	return AllocationRequest{}
}
