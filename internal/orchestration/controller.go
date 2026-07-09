package orchestration

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/SovereignAI/internal/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func NewController(cdClient CDClient, config OrchestratorConfig) *SovereignWorkflowController {
	return &SovereignWorkflowController{
		CDClient: cdClient,
		Config:   config,
	}
}

// Connect reconciler to Kubernetes event streaming for pods
func (c *SovereignWorkflowController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Watch pods owned by the SovereignWorkflow CRD
		For(&v1alpha1.SovereignWorkflow{}).
		Owns(&corev1.Pod{}).

		// Watches unowned shared vLLM server pools via label maps
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(c.mapVLLMPodToWorkflows),
			builder.WithPredicates(vllmLabelPredicate()),
		).

		// Watch GPU CRDs for VRAM Pressure Events, enqueue Workflows affected
		Watches(
			&v1alpha1.GPUNode{},
			handler.EnqueueRequestsFromMapFunc(c.findWorkflowsRunningOnPressuredGPU),
			builder.WithPredicates(vramPressurePredicate()), // Only execute mapping func if vram pressure warning appears
		).
		Complete(c)
}

func (c *SovereignWorkflowController) Stop(ctx context.Context) {
	//TODO
}

// Reconcile loop verifies pod status and forwards tasks to the appropriate handler
func (c *SovereignWorkflowController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Get Workflow to be reconciled
	var sw v1alpha1.SovereignWorkflow
	if err := c.Client.Get(ctx, req.NamespacedName, &sw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Reclaim zombie inference points if it hasn't been used in a while
	if err := c.reclaimZombieInference(ctx, sw.Status.AllocatedNodeName); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reclaim zombie inference: %w", err)
	}

	// Handle GPU VRAM pressure
	handled, err := c.reconcileGpuVramPressure(ctx, &sw)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("gpu vram pressure reconcile: %w", err)
	} else if handled {
		return ctrl.Result{}, nil
	}

	// Standard progression
	return c.reconcileWorkflowProgress(ctx, &sw)
}

// Collect+Trigger workflows using this vLLM Pod
func (c *SovereignWorkflowController) mapVLLMPodToWorkflows(ctx context.Context, obj client.Object) []reconcile.Request {
	// Get vLLM pod
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil // Not a Pod
	}

	// Ensure this is a vLLM pod
	if pod.Labels["app.kubernetes.io/name"] != "vllm-server" {
		return nil
	}
	gpuIndex := pod.Labels["sovereign-ai.io/gpu-index"] // Grab your placement index straight from the pod

	requests := make(map[reconcile.Request]struct{})

	// Get Workflows that are tied to this GPU CRD
	var crd v1alpha1.GPUNode
	nodeName := pod.Spec.NodeName

	// If no node was assigned, scheduling problem
	if pod.Spec.NodeName == "" || gpuIndex == "" {
		log.Printf("no node assigned to vLLM pod")
		return nil
	}

	// No CRD exists for vLLM, implies Node died/disconnected.
	// Need to get affected workflows based on node name
	err := c.Client.Get(ctx, types.NamespacedName{Name: nodeName}, &crd)
	if err != nil || crd.DeletionTimestamp != nil {
		// Other error
		if !apierrors.IsNotFound(err) {
			log.Printf("failed to find GPU CRD due to other failure: %v", err)
			return nil
		}

		// Get every workflow assigned to this node
		var matchingWorkflows v1alpha1.SovereignWorkflowList
		err := c.Client.List(ctx, &matchingWorkflows, client.MatchingFields{"status.assignedNode": nodeName})
		if err != nil {
			return nil
		}

		for _, sw := range matchingWorkflows.Items {
			stepStatus := c.getStepStatus(&sw, sw.Status.ActiveStepName)

			// Exact match isolation: Did this workflow use this exact GPU index?
			if stepStatus.GPUIndex == gpuIndex {
				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: sw.Namespace,
						Name:      sw.Name,
					},
				}
				requests[req] = struct{}{}
			}
		}

		// Convert map to slice and return
		var reconcileRequests []reconcile.Request
		for req := range requests {
			reconcileRequests = append(reconcileRequests, req)
		}
		return reconcileRequests
	}

	// Get workflows based on inference endpoint
	for _, endpoints := range crd.Status.ActiveInferenceEndpoints {
		if endpoints.GPUIndex == gpuIndex {
			// Get workflows tied to this gpu instance
			// Get every workflow assigned to this node
			var matchingWorkflows v1alpha1.SovereignWorkflowList
			err := c.Client.List(ctx, &matchingWorkflows, client.MatchingFields{"status.assignedNode": nodeName})
			if err != nil {
				return nil
			}

			// TODO: this is terrible
			for _, wf := range matchingWorkflows.Items {
				// Get active step
				activeStepName := wf.Status.ActiveStepName
				for _, step := range wf.Status.StepStatuses {
					if step.StepName == activeStepName {
						if step.GPUIndex == gpuIndex {
							req := reconcile.Request{
								NamespacedName: types.NamespacedName{
									Namespace: wf.Namespace,
									Name:      wf.Name,
								},
							}
							requests[req] = struct{}{}
						}
					}
				}
			}
			// Convert map to slice and return
			var reconcileRequests []reconcile.Request
			for req := range requests {
				reconcileRequests = append(reconcileRequests, req)
			}
			return reconcileRequests
		}
	}

	// Crashed vLLM pod has no active inference endpoints, it will get reclaimed later
	return nil
}

// Collect+Trigger workflows running on GPU giving VRAM pressure change
func (c *SovereignWorkflowController) findWorkflowsRunningOnPressuredGPU(ctx context.Context, obj client.Object) []reconcile.Request {
	gpuNode, ok := obj.(*v1alpha1.GPUNode)
	if !ok {
		return nil
	}

	// Get all Pods owned by the orchestrator (Workflow pods) that are running on this node
	var podList corev1.PodList
	err := c.Client.List(ctx, &podList,
		client.MatchingFields{"spec.nodeName": gpuNode.Spec.NodeName},
		client.MatchingLabels{"app.kubernetes.io/managed-by": "sovereign-orchestrator"},
	)
	if err != nil {
		// If we have VRAM pressure but no pods, need to log as an error. A non-owned workload is causing pressure
		if gpuNode.Status.HasVRAMPressure {
			log.Printf("GPU node %s has VRAM pressure but no managed pods were found", gpuNode.Spec.NodeName)
		}
		return nil
	}

	// Extract unique Workflows that own these Pods
	requests := make(map[types.NamespacedName]struct{})
	for _, pod := range podList.Items {
		for _, owner := range pod.OwnerReferences {
			if owner.Kind == "SovereignWorkflow" {
				req := types.NamespacedName{
					Name:      owner.Name,
					Namespace: pod.Namespace,
				}
				requests[req] = struct{}{} // Using map to deduplicate
			}
		}
	}

	// Convert map to slice of reconcile.Requests
	var reconcileRequests []reconcile.Request
	for req := range requests {
		reconcileRequests = append(reconcileRequests, reconcile.Request{NamespacedName: req})
	}

	// Returning this slice automatically queues Reconcile() for these specific Workflows
	return reconcileRequests
}

func (c *SovereignWorkflowController) reclaimZombieInference(ctx context.Context, nodeName string) error {
	if nodeName == "" {
		return nil
	}
	// Get the current physical Reality
	var podList corev1.PodList
	if err := c.Client.List(ctx, &podList, client.MatchingLabels{"app.kubernetes.io/name": "vllm-server"}); err != nil {
		return err
	}

	// Get the current Ledger State
	latestNode := &v1alpha1.GPUNode{}
	if err := c.Client.Get(ctx, types.NamespacedName{Name: nodeName}, latestNode); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Reconcile the differences (Reality overwrites Bookkeeping)
	for _, endpoint := range latestNode.Status.ActiveInferenceEndpoints {

		// If we find a matching pod, inference is still active
		matchingPod := findPodByURL(podList, endpoint.EndpointURL)
		if matchingPod == nil {
			// The physical pod was deleted or crashed out of existence!
			// Purge it completely from the VRAM allocation totals.
			latestNode.Status.AllocatedVRAM -= endpoint.AllocatedVRAM
			purgeEndpointEntry(&latestNode.Status.ActiveInferenceEndpoints, endpoint.EndpointURL)
		}
	}
	return c.Client.Status().Update(ctx, latestNode)
}

func findPodByURL(podList corev1.PodList, endpointURL string) *corev1.Pod {
	for i := range podList.Items {
		pod := &podList.Items[i]

		if pod.Status.PodIP == "" {
			continue
		}

		if strings.Contains(endpointURL, pod.Status.PodIP) || strings.Contains(endpointURL, pod.Name) {
			return pod
		}
	}
	return nil
}

// Removes an entry from the ActiveInferenceEndpoints slice matching the URL,
// modifying the underlying slice in-place.
func purgeEndpointEntry(endpoints *[]v1alpha1.LegacyInferenceEndpoint, targetURL string) {
	if endpoints == nil {
		return
	}

	// Create a new slice slice index tracker targeting the same underlying array
	n := 0
	for _, ep := range *endpoints {
		// Keep everything that does NOT match the zombie URL
		if ep.EndpointURL != targetURL {
			(*endpoints)[n] = ep
			n++
		}
	}

	// Truncate the slice to drop the removed end elements and trim allocations
	*endpoints = (*endpoints)[:n]
}
