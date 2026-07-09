package orchestration

import (
	"context"
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (c *SovereignWorkflowController) reconcileGpuVramPressure(ctx context.Context, sw *v1alpha1.SovereignWorkflow) (bool, error) {
	// Identify node assigned to request
	activeNode := sw.Status.AllocatedNodeName
	if activeNode == "" {
		return false, nil // Nothing running, nothing to do
	}

	// Get GPU CRD to verify VRAM pressure signal
	gpuNode := &v1alpha1.GPUNode{}
	if err := c.Client.Get(ctx, types.NamespacedName{Name: activeNode}, gpuNode); err != nil {
		return false, err
	}

	isUnderPressure := gpuNode.Status.HasVRAMPressure

	// If we are under pressure and running, we must execute the policy assigned to the Workflow/Step
	// Otherwise, we need to check if the Workflow is paused and resume it
	if isUnderPressure && sw.Status.Phase == "Running" {
		// VRAM is full, pause for now.
		// TODO: Implement actual freezing logic
		// err := c.executeSIGSTOP(ctx, wf)
		sw.Status.Phase = "PausedForVRAM"
		c.Client.Status().Update(ctx, sw)
		return true, nil
	} else if !isUnderPressure && sw.Status.Phase == "PausedForVRAM" {
		// VRAM is clear, and we are currently paused. We need to resume.
		// TODO: Implement actual unfreezing logic
		// err := c.executeSIGCONT(ctx, wf)
		sw.Status.Phase = "Running"
		c.Client.Status().Update(ctx, sw)
		return true, nil
	}

	// No VRAM issue, exit
	return false, nil
}

func (c *SovereignWorkflowController) reconcileWorkflowProgress(ctx context.Context, sw *v1alpha1.SovereignWorkflow) (ctrl.Result, error) {
	// Migrate pending workflow to running
	if sw.Status.Phase == "" {
		if err := c.handleWorkflowPendingToRunning(ctx, sw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to push pending Workflow %s to running: %w", sw.Name, err)
		}

		// Update Kubernetes CRD
		return ctrl.Result{}, c.Client.Status().Update(ctx, sw)
	}

	// Get active step status
	activeStepName := sw.Status.ActiveStepName
	stepStatus := c.getStepStatus(sw, activeStepName)

	switch stepStatus.Phase {
	case v1alpha1.StepPhasePending:
		return c.handleStepPending(ctx, sw)
	case v1alpha1.StepPhaseRunning:
		return c.handleRunningStep(ctx, sw, stepStatus)
	case v1alpha1.StepPhaseFailed:
		// failed
	case v1alpha1.StepPhaseIntervention:
		// Pause agent?
	}

	return ctrl.Result{}, c.Client.Status().Update(ctx, sw)
}
