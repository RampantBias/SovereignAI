package v1alpha1

import "fmt"

// ApprovalRequirement names the earlier human gate and shared artifact input.
type ApprovalRequirement struct {
	Step    string `json:"step"`
	Subject string `json:"subject"`
}

// ResolvedApproval pins the human authority used to admit a utility operation.
type ResolvedApproval struct {
	Requirement      ApprovalRequirement `json:"requirement"`
	RequestRef       UIDReference        `json:"requestRef"`
	DecisionRef      UIDReference        `json:"decisionRef"`
	Subject          ArtifactReference   `json:"subject"`
	AdmissionEventID string              `json:"admissionEventId"`
}

// ValidateApprovalRequirements is shared by API ingress and controller admission.
func ValidateApprovalRequirements(steps []StepConfig) error {
	byName := make(map[string]int, len(steps))
	for i, step := range steps {
		if _, exists := byName[step.Name]; exists {
			return fmt.Errorf("duplicate step %q", step.Name)
		}
		byName[step.Name] = i
	}
	for i, step := range steps {
		required := step.RequiresApproval
		if step.Utility != nil && step.Utility.Name == "git.mergeRequest" && required == nil {
			return fmt.Errorf("step %q requires an explicit approval binding", step.Name)
		}
		if required == nil {
			continue
		}
		gateIndex, exists := byName[required.Step]
		if step.Kind != ExecutionKindUtility || required.Subject == "" || !exists || gateIndex >= i || steps[gateIndex].Order >= step.Order || steps[gateIndex].Kind != ExecutionKindHumanGate {
			return fmt.Errorf("step %q requires an earlier HumanGate and a shared subject input", step.Name)
		}
		if !hasApprovalInput(step.Inputs, required.Subject) || !hasApprovalInput(steps[gateIndex].Inputs, required.Subject) {
			return fmt.Errorf("approval subject %q must be an input of both %q and %q", required.Subject, step.Name, required.Step)
		}
	}
	return nil
}
func hasApprovalInput(inputs []ArtifactReference, name string) bool {
	count := 0
	for _, input := range inputs {
		if input.Name == name {
			count++
		}
	}
	return count == 1
}
