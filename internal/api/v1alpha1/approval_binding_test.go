package v1alpha1

import "testing"

func TestApprovalRequirementDefinition(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]StepConfig)
		valid  bool
	}{
		{"valid", func([]StepConfig) {}, true},
		{"missing binding", func(s []StepConfig) { s[1].RequiresApproval = nil }, false},
		{"missing gate", func(s []StepConfig) { s[1].RequiresApproval.Step = "absent" }, false},
		{"self reference", func(s []StepConfig) { s[1].RequiresApproval.Step = "request" }, false},
		{"later order", func(s []StepConfig) { s[0].Order = 3 }, false},
		{"non human gate", func(s []StepConfig) { s[0].Kind = ExecutionKindAgent }, false},
		{"missing reviewed subject", func(s []StepConfig) { s[0].Inputs = nil }, false},
		{"missing operation subject", func(s []StepConfig) { s[1].Inputs = nil }, false},
		{"duplicate step", func(s []StepConfig) { s[1].Name = s[0].Name }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []StepConfig{{Name: "gate", Kind: ExecutionKindHumanGate, Order: 1, Inputs: []ArtifactReference{{Name: "candidate-revision"}}},
				{Name: "request", Kind: ExecutionKindUtility, Order: 2, Utility: &UtilityOperationRequest{Name: "git.mergeRequest"}, Inputs: []ArtifactReference{{Name: "candidate-revision"}}, RequiresApproval: &ApprovalRequirement{Step: "gate", Subject: "candidate-revision"}}}
			tc.mutate(steps)
			if err := ValidateApprovalRequirements(steps); (err == nil) != tc.valid {
				t.Fatalf("validation=%v", err)
			}
		})
	}
}
