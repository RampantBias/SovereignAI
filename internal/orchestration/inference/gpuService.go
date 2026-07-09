package inference

import (
	"context"
	"errors"
	"math"

	"github.com/SovereignAI/internal/api/v1alpha1"
	orc "github.com/SovereignAI/internal/orchestration"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Errors
var (
	ErrNoCapacity = errors.New("no GPU nodes have sufficient available VRAM")
	ErrConflict   = errors.New("optimistic lock conflict during GPU allocation")
)

func NewService(reader client.Reader, client client.Client) *inferenceService {
	return &inferenceService{
		apiReader: reader,
		client:    client,
	}
}

// FindPlacement evaluates the cluster and returns the best Node/GPU for a new request.
func (s *inferenceService) FindPlacement(ctx context.Context, req orc.AllocationRequest) (orc.PlacementResult, error) {
	var nodeList v1alpha1.GPUNodeList

	// Get node list directly from API (avoid cache)
	if err := s.apiReader.List(ctx, &nodeList); err != nil {
		return orc.PlacementResult{}, err
	}

	// Warm Start
	// First, we need to see if any node item has the same model loaded and enough free VRAM to reserve KV cache
	for _, node := range nodeList.Items {
		// Get available VRAM
		effectiveUsed := int64(math.Max(float64(node.Status.AllocatedVRAM), float64(node.Status.UsedVRAM)))
		available := node.Spec.TotalVRAM - effectiveUsed

		// Check each inference endpoint, if we find a matching model attempt to schedule agent there
		for _, inference := range node.Status.ActiveInferenceEndpoints {
			if inference.ModelID == req.ModelName && available >= req.RequiredKVVRAM {
				if available >= req.RequiredKVVRAM {
					return orc.PlacementResult{
						NodeName:    node.Spec.NodeName,
						GPUIndex:    inference.GPUIndex,
						EndpointURL: inference.EndpointURL,
						Action:      "Reuse",
					}, nil
				}
			}
		}
	}

	// Cold Start
	// Find node GPU that can load both model + expected KV Cache
	for _, node := range nodeList.Items {
		// Calculate used VRAM based on the higher of the two: (allocated || used)
		effectiveUsed := int64(math.Max(float64(node.Status.AllocatedVRAM), float64(node.Status.UsedVRAM)))
		available := node.Spec.TotalVRAM - effectiveUsed

		if available >= req.RequiredKVVRAM+req.RequiredModelVRAM {
			return orc.PlacementResult{
				NodeName:    node.Name,
				GPUIndex:    "0",
				Action:      "Create",
				EndpointURL: "TODO-PLACEHOLDER",
			}, nil
		}
	}

	// No nodes available
	return orc.PlacementResult{}, ErrNoCapacity
}

func (s *inferenceService) allocateNewEndpoint(ctx context.Context, node v1alpha1.GPUNode, req orc.AllocationRequest) (orc.PlacementResult, error) {
	// Update allocated VRAM and add workflow
	node.Status.AllocatedVRAM += req.RequiredKVVRAM
	node.Status.ActiveWorkflows = append(node.Status.ActiveWorkflows, req.WorkflowID)

	// Update GPU CRD
	err := s.client.Status().Update(ctx, &node)
	if err != nil {
		if apierrors.IsConflict(err) {
			// Another orchestrator thread updated this node in the last millisecond.
			return orc.PlacementResult{}, ErrConflict
		}
		return orc.PlacementResult{}, err
	}

	return orc.PlacementResult{
		NodeName: node.Name,
		GPUIndex: "0", // Hardcoded to 0 for MVP
	}, nil
}

// ReleaseAllocation removes a subscription when a workflow or step completes/terminates.
func (svc *inferenceService) ReleaseAllocation(ctx context.Context, nodeName string, gpuIndex int, workflowID string) error {
	return nil
}
