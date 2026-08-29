package controllers

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

func TestAgentRuntimeSmokeStopsAfterThreeSingleOutputAgents(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "smoke-agent-runtime-workflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow v1alpha1.SovereignWorkflow
	if err := yaml.UnmarshalStrict(data, &workflow); err != nil {
		t.Fatalf("decode agent runtime smoke workflow: %v", err)
	}
	if len(workflow.Spec.Steps) != 4 || workflow.Spec.Steps[0].Kind != v1alpha1.ExecutionKindUtility {
		t.Fatalf("smoke workflow must contain repository initialization and three agents")
	}
	want := []v1alpha1.ContractReference{
		{Name: "implementation-plan", Version: "v1"},
		{Name: "test-change-set", Version: "v1"},
		{Name: "change-set", Version: "v1"},
	}
	for index, output := range want {
		step := workflow.Spec.Steps[index+1]
		if step.Kind != v1alpha1.ExecutionKindAgent || step.Agent == nil || step.Agent.Inference == nil ||
			step.Agent.Image != "sovereign-reference-agent:dev" || step.MaxAttempts != 5 ||
			len(step.Outputs) != 1 || step.Outputs[0] != output {
			t.Fatalf("step %q is not a retry-bounded reference-agent producing %#v", step.Name, output)
		}
		for _, capability := range []string{agentcontract.CapabilityWorkspaceRead, agentcontract.CapabilityAgentComplete} {
			if !slices.Contains(step.Agent.Capabilities, capability) {
				t.Errorf("step %q is missing capability %q", step.Name, capability)
			}
		}
	}
	if !slices.Contains(workflow.Spec.Steps[1].Agent.Capabilities, agentcontract.CapabilityCandidateWrite) {
		t.Error("architect is missing candidate_write")
	}
	for _, index := range []int{2, 3} {
		if !slices.Contains(workflow.Spec.Steps[index].Agent.Capabilities, agentcontract.CapabilityWorkspaceWrite) {
			t.Errorf("step %q is missing workspace_write", workflow.Spec.Steps[index].Name)
		}
		if slices.Contains(workflow.Spec.Steps[index].Agent.Capabilities, agentcontract.CapabilityWorkspaceReplace) {
			t.Errorf("step %q unexpectedly exposes workspace_replace", workflow.Spec.Steps[index].Name)
		}
	}
	testAuthor := workflow.Spec.Steps[2].Agent.Responsibility
	for _, expected := range []string{
		"directly in the attempt workspace",
		"Modify exactly one existing file: src/main_test.go",
		"Never include src/main.go",
		"Do not generate or return a diff or patchLines",
	} {
		if !strings.Contains(testAuthor, expected) {
			t.Errorf("test-author responsibility does not contain %q", expected)
		}
	}
	developer := workflow.Spec.Steps[3].Agent.Responsibility
	for _, expected := range []string{
		"directly in the attempt workspace",
		"Modify exactly one existing file: src/main.go",
		"Never include src/main_test.go",
		"Do not generate or return a diff or patchLines",
	} {
		if !strings.Contains(developer, expected) {
			t.Errorf("developer responsibility does not contain %q", expected)
		}
	}
}
