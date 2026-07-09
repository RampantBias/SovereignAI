package orchestration

import (
	"context"
	"fmt"
	"log"
	"slices"

	"github.com/SovereignAI/internal/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (c *SovereignWorkflowController) handleWorkflowPendingToRunning(ctx context.Context, sw *v1alpha1.SovereignWorkflow) error {

	// Build default network policy
	networkPolicy := buildNetworkPolicy(sw.Namespace)

	// Build PVC

	// Build SAs

	// Create Network Policy

	// Bind network policy to Workflow
	if err := controllerutil.SetControllerReference(sw, networkPolicy, c.Scheme); err != nil {
		return fmt.Errorf("failed to bind default network policy owner reference: %w", err)
	}
	if err := c.Client.Create(ctx, networkPolicy); err != nil {
		return fmt.Errorf("failed to create Workflow default network policy: %w", err)
	}

	// Set Workflow to Running status and set active step name
	sw.Status.Phase = "Running"
	sw.Status.ActiveStepName = sw.Spec.Steps[0].Name
	return nil
}

func (c *SovereignWorkflowController) handleStepPending(ctx context.Context, sw *v1alpha1.SovereignWorkflow) (ctrl.Result, error) {
	activeStepName := sw.Status.ActiveStepName

	// Ensure PVC is running or operating
	if err := c.ensureStorage(ctx, sw, activeStepName); err != nil {
		return ctrl.Result{}, err
	}

	// Ask GpuService for placement
	placement, err := c.ensureInference(ctx, sw)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Check Pod status to verify actual workload is not yet running
	var existingPod corev1.Pod
	podName := fmt.Sprintf("%s-%s", sw.Spec.WorkflowID, activeStepName)
	err = c.Client.Get(ctx, types.NamespacedName{Namespace: sw.Namespace, Name: podName}, &existingPod)

	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("failed to check for existing agent pod: %w", err)
	}

	// Create pod
	if apierrors.IsNotFound(err) {
		pod, err := c.buildAgentPod(sw, activeStepName, placement)
		if err != nil {
			return ctrl.Result{}, err
		}

		if err := c.Client.Create(ctx, pod); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create agent pod: %w", err)
		}
	}

	// Update step status to Running
	c.updateStepPhase(sw, activeStepName, v1alpha1.StepPhaseRunning, placement)
	return ctrl.Result{}, c.Client.Status().Update(ctx, sw)
}

func (c *SovereignWorkflowController) handleRunningStep(ctx context.Context, sw *v1alpha1.SovereignWorkflow, stepStatus v1alpha1.StepRuntimeStatus) (ctrl.Result, error) {
	// Check if the pod completed or needs intervention
	var pod corev1.Pod
	err := c.Client.Get(ctx, types.NamespacedName{Namespace: sw.Namespace, Name: stepStatus.PodName}, &pod)
	if err != nil {
		// Handle missing pod / intervention required
		return ctrl.Result{}, err
	}

	// Verify inference engine is still alive
	var inferencePod corev1.Pod
	err = c.Client.Get(ctx, types.NamespacedName{
		Namespace: sw.Namespace,
		Name:      stepStatus.InferencePodName,
	}, &inferencePod)

	// Handle inference engine failure
	if apierrors.IsNotFound(err) || (err == nil && inferencePod.Status.Phase == corev1.PodFailed) {
		// The inference engine serving this running agent has died mid-flight
		return c.handleInferenceFailure(ctx, pod, sw, stepStatus)
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to verify inference engine health: %w", err)
	}

	if pod.Status.Phase == corev1.PodSucceeded {
		// Step finished successfully! Move to next step or complete workflow
		nextStep, exists := c.getNextStep(sw, stepStatus.StepName)
		if !exists {
			sw.Status.Phase = "Completed"
			sw.Status.ActiveStepName = ""
		} else {
			sw.Status.ActiveStepName = nextStep.Name
		}
		return ctrl.Result{}, c.Client.Status().Update(ctx, sw)
	}

	return ctrl.Result{}, nil
}

func (c *SovereignWorkflowController) handleInferenceFailure(ctx context.Context, pod corev1.Pod, sw *v1alpha1.SovereignWorkflow, stepStatus v1alpha1.StepRuntimeStatus) (ctrl.Result, error) {
	//  Clear out the corrupted step state, forcing it back to an unassigned status
	c.clearStepInferenceBinding(ctx, pod, sw)

	// Force a clean infrastructure reschedule by flipping it back to Pending
	// The next reconciliation loop will execute handleStepPending, call FindPlacement,
	// and cleanly request VRAM allocation on a healthy, alternative GPU node.
	stepStatus.Phase = v1alpha1.StepPhasePending

	// TODO: Integrate context engine to summarize prior inference results (from logs)

	// Update Workflow CRD to trigger next Reconcile/restore
	return ctrl.Result{}, c.Client.Status().Update(ctx, sw)
}

func (c *SovereignWorkflowController) clearStepInferenceBinding(ctx context.Context, pod corev1.Pod, sw *v1alpha1.SovereignWorkflow) (ctrl.Result, error) {
	stepStatus := getStepRunetimeStatus(sw, sw.Status.ActiveStepName)
	nodeName := sw.Status.AllocatedNodeName

	// Update the GPUNode CRD (if available) by removing the Workflow ID
	if nodeName != "" {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Pull a fresh, un-cached version of the node configuration
			var gpuNode v1alpha1.GPUNode
			if err := c.Client.Get(ctx, types.NamespacedName{Name: nodeName}, &gpuNode); err != nil {
				if apierrors.IsNotFound(err) {
					return nil // Node dead & deleted. Ledger is reaped natively, skip.
				}
				return err
			}

			// Cleanse top-level ActiveWorkflows array using linear index searches
			if idx := slices.Index(gpuNode.Status.ActiveWorkflows, sw.Spec.WorkflowID); idx != -1 {
				gpuNode.Status.ActiveWorkflows = slices.Delete(gpuNode.Status.ActiveWorkflows, idx, idx+1)
			}

			// Cleanse nested ActiveInferenceEndpoints workloads
			for i, inference := range gpuNode.Status.ActiveInferenceEndpoints {
				if inference.EndpointURL == stepStatus.InferenceEndpointURL {
					if innerIdx := slices.Index(inference.ActiveWorkflows, sw.Spec.WorkflowID); innerIdx != -1 {
						gpuNode.Status.ActiveInferenceEndpoints[i].ActiveWorkflows = slices.Delete(
							inference.ActiveWorkflows, innerIdx, innerIdx+1,
						)
					}
				}
			}

			// Save the ledger modifications back to the cluster control plane
			return c.Client.Status().Update(ctx, &gpuNode)
		})

		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to clear hardware ledger lease: %w", err)
		}
	}

	// Forcefully delete the isolated agent pod container
	if pod.Name != "" && pod.Namespace != "" {
		if err := c.Client.Delete(ctx, &pod); err != nil && !apierrors.IsNotFound(err) {
			log.Printf("failed to delete agent pod that lost inference engine: %v", err)
			return ctrl.Result{}, err
		}
	}

	// Cleanse control plane step status
	for i, step := range sw.Status.StepStatuses {
		if step.StepName == sw.Status.ActiveStepName {
			sw.Status.StepStatuses[i].AssignedNode = ""
			sw.Status.StepStatuses[i].InferenceEndpointURL = ""
			sw.Status.StepStatuses[i].GPUIndex = ""
			sw.Status.StepStatuses[i].InferencePodName = ""
			sw.Status.StepStatuses[i].PodName = ""
			sw.Status.StepStatuses[i].Phase = v1alpha1.StepPhasePending // Flag as Pending for the next loop run
		}
	}

	return ctrl.Result{}, nil
}

func GetStepConfig(sw *v1alpha1.SovereignWorkflow, activeStepName string) v1alpha1.StepConfig {
	for _, step := range sw.Spec.Steps {
		if step.Name == activeStepName {
			return step
		}
	}
	return v1alpha1.StepConfig{}
}

func getStepRunetimeStatus(sw *v1alpha1.SovereignWorkflow, activeStepName string) v1alpha1.StepRuntimeStatus {
	for _, step := range sw.Status.StepStatuses {
		if step.StepName == activeStepName {
			return step
		}
	}
	return v1alpha1.StepRuntimeStatus{}
}

func (c *SovereignWorkflowController) getNextStep(sw *v1alpha1.SovereignWorkflow, activeStepName string) (v1alpha1.StepConfig, bool) {
	for i, status := range sw.Status.StepStatuses {
		if status.StepName == activeStepName {
			if i+1 < len(sw.Spec.Steps) {
				return sw.Spec.Steps[i+1], true
			}
		}
	}
	return v1alpha1.StepConfig{}, false
}

func (c *SovereignWorkflowController) updateStepPhase(sw *v1alpha1.SovereignWorkflow, stepName string, phase v1alpha1.StepPhase, placement PlacementResult) {
	found := false
	for i, status := range sw.Status.StepStatuses {
		if status.StepName == stepName {
			sw.Status.StepStatuses[i].Phase = phase
			sw.Status.StepStatuses[i].AssignedNode = placement.NodeName
			sw.Status.StepStatuses[i].GPUIndex = placement.GPUIndex
			sw.Status.StepStatuses[i].InferencePodName = placement.InferencePodName
			found = true
			break
		}
	}

	if !found {
		sw.Status.StepStatuses = append(sw.Status.StepStatuses, v1alpha1.StepRuntimeStatus{
			StepName:     stepName,
			Phase:        phase,
			AssignedNode: placement.NodeName,
			GPUIndex:     placement.GPUIndex,
			PodName:      sw.Name + "-" + stepName, // Deterministic pod naming rule
		})
	}
}

// Handle a Pending task by starting the workflow
// TODO: Add rollback handling
// func (c *SovereignWorkflowController) handlePendingWorkflowLegacy(ctx context.Context, workflow *core.Workflow) {
// 	log.Printf("Bootstrapping workflow %s in namespace %s", workflow.ID, workflow.Namespace)

// 	// Create new namespace for task
// 	if err := c.manager.CreateNamespace(ctx, workflow.Namespace); err != nil {
// 		log.Printf("Failed to create namespace: %v", err)
// 		return
// 	}

// 	// Create new PVC based on storage class name defined in config
// 	pvcConfig := PvcConfig{
// 		Namespace:        workflow.Namespace,
// 		Name:             workflow.PvcName,
// 		StorageClassName: c.Config.StorageClassName,
// 		StorageSize:      "100Mi",
// 	}

// 	if err := c.manager.CreatePVC(ctx, pvcConfig); err != nil {
// 		log.Printf("Failed to create pvc: %v", err)
// 		return
// 	}

// 	// Generate local SA + RB
// 	if err := c.manager.CreateAgentRBAC(ctx, workflow.Namespace); err != nil {
// 		log.Printf("Failed to create workflow %s RBAC permissions: %v", workflow.Namespace, err)
// 		return
// 	}

// 	// Generate Deny All policy
// 	if err := c.manager.CreateAgentNetworkPolicy(ctx, workflow.Namespace); err != nil {
// 		log.Printf("Failed to create workflow %s Network Policy: %v", workflow.Namespace, err)
// 		return
// 	}

// 	// Update task status (we verify the status has not changed since we last locked)
// 	workflow.UpdateWorkflowStatus(core.WorkflowPending, core.WorkflowActive)
// }

// ///***************************
// /// Step Routines
// ///***************************

// func (c *SovereignWorkflowController) handlePendingStepLegacy(ctx context.Context, workflow *core.Workflow, step *core.Step) {
// 	// Verify PVC is Bound
// 	storageStatus, err := c.manager.GetPVCStatus(ctx, workflow)
// 	if err != nil || storageStatus != PvcBound {
// 		log.Printf("PVC for workflow %s has not yet been bound.", workflow.Namespace)
// 		return
// 	}

// 	log.Printf("Bootstrapping step %s in namespace %s", step.Name, workflow.Namespace)

// 	// Create step directory
// 	hostPath, err := c.manager.GetHostPath(ctx, workflow.Namespace, workflow.PvcName)
// 	if err != nil {
// 		log.Printf("Unable to obtain host path for workflow %s and step %s!", workflow.Namespace, step.Name)
// 		return
// 	}
// 	os.MkdirAll(filepath.Join(hostPath, step.PodName), 0777)

// 	// Write task file
// 	c.writeTaskFile(ctx, workflow, step)

// 	// Determine VRAM requirement and available node (assume ModelName is valid)
// 	// requiredVRAM, err := o.GetModelVramRequirement(step.ModelName)
// 	// if err != nil {
// 	// 	step.Status = core.StepFailed
// 	// 	step.Message = err.Error()
// 	// 	return
// 	// }

// 	nodeName, gpuID, err := "", "", nil // o.registry.Ledger.FindAvailableNode(requiredVRAM)
// 	if err != nil {
// 		step.Status = core.StepStalled
// 		step.Message = "Insufficient VRAM in cluster"
// 		return
// 	}

// 	// Create Agent Pod
// 	podConfig := PodConfig{
// 		Namespace:       workflow.Namespace,
// 		WorkflowID:      workflow.ID,
// 		StepName:        step.PodName,
// 		AgentImage:      c.Config.AgentImage,
// 		ImagePullPolicy: c.Config.ImagePullPolicy,
// 		NodeName:        nodeName,
// 		GPUID:           gpuID,
// 	}

// 	if err := c.manager.CreateAgentPod(ctx, podConfig); err != nil {
// 		log.Printf("Failed to create agent pod: %v", err)
// 		return
// 	}

// 	// Move status from pending to running
// 	workflow.UpdateStepStatus(workflow.CurrentStep, core.StepPending, core.StepRunning)
// }

// func (c *SovereignWorkflowController) writeTaskFile(ctx context.Context, workflow *core.Workflow, step *core.Step) error {

// 	// Create task to submit to orchestrator
// 	task := Task{
// 		StepName:   step.Name,
// 		WorkflowID: workflow.ID,
// 		Goal:       "PLACEHOLDER",
// 		CreatedAt:  time.Now(),
// 	}

// 	// Convert task to json format
// 	taskAsJson, err := json.MarshalIndent(task, "", " ")
// 	if err != nil {
// 		return err
// 	}

// 	// Get PVC physical path
// 	pvcPath := c.manager.GetPVCPath(ctx, workflow.Namespace)
// 	taskFilePath := filepath.Join(pvcPath, "task.json")
// 	tempTaskFilePath := taskFilePath + ".tmp"

// 	// Write file
// 	if os.WriteFile(tempTaskFilePath, taskAsJson, 0666) != nil {
// 		log.Printf("Failed to write task.json for workflow %s at step %s", workflow.Namespace, step.Name)
// 		return err
// 	}

// 	// Rename. We rename to take advantage of atomicity of operating system, to prevent agent partial reads of task.json
// 	os.Rename(tempTaskFilePath, taskFilePath)
// 	return nil
// }

// // Determine if pod is still operating, succeeded, or failed
// func (c *SovereignWorkflowController) handleRunningStep(ctx context.Context, workflow *core.Workflow, step *core.Step) {
// 	// Get pod status
// 	status, err := c.manager.GetPodStatus(ctx, workflow.Namespace, step.Name)
// 	if err != nil {
// 		log.Printf("Error checking pod %s: %v", step.Name, err)
// 		return
// 	}

// 	// Logic for terminal infra failures
// 	// if isTerminalInfraFailure(status) {
// 	// 	log.Printf("Terminal infra failure detected for %s: %s", step.Name, status)
// 	// 	workflow.UpdateStepStatus(workflow.CurrentStep, StepRunning, StepFailed)
// 	// 	o.cleanupStepResources(workflow, step)
// 	// 	return
// 	// }

// 	// Evaluate if pod has succeeded or failed, ensure status is still set to running
// 	switch status {
// 	case "Succeeded":
// 		log.Printf("Task %s completed. Verifying result...", step.Name)

// 		// Verify existence of status.json
// 		pvcPath := c.manager.GetPVCPath(ctx, workflow.Namespace)
// 		statusData, err := os.ReadFile(filepath.Join(pvcPath, "status.json"))
// 		if err != nil {
// 			log.Printf("Pod succeeded but status.json is missing for task %s", step.Name)
// 			workflow.UpdateStepStatus(workflow.CurrentStep, core.StepRunning, core.StepIntervention)
// 			return
// 		}

// 		// Parse agent's report
// 		var agentStatus Status
// 		if err := json.Unmarshal(statusData, &agentStatus); err != nil {
// 			workflow.UpdateStepStatus(workflow.CurrentStep, core.StepRunning, core.StepIntervention)
// 			return
// 		}

// 		// Report and update step status with required step
// 		log.Printf("Task %s completed successfully. Agent Message: %s", step.Name, agentStatus.Message)

// 		if step.RequiresReview {
// 			workflow.UpdateStepStatus(workflow.CurrentStep, core.StepRunning, core.StepIntervention)
// 		} else {
// 			workflow.UpdateStepStatus(workflow.CurrentStep, core.StepRunning, core.StepCompleted)
// 		}
// 	case "Failed":
// 		log.Printf("Task %s failed", step.Name)
// 		workflow.UpdateStepStatus(workflow.CurrentStep, core.StepRunning, core.StepIntervention)
// 		//o.cleanupFailedStep(workflow, step)
// 	}
// }

// func (c *SovereignWorkflowController) handleInterventionStep(ctx context.Context, workflow *core.Workflow, step *core.Step) {
// 	// We do nothing here.
// 	// The task stays in this state until a human (via the CLI)
// 	// pushes it to the next status.

// 	// Optional: Log once every N ticks to show it's still "alive"
// 	// but don't spam your logs.
// }
