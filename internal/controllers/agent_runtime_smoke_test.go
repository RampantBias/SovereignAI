package controllers

import (
	"os"
	"path/filepath"
	"testing"

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
			step.Agent.Image != "sovereign-reference-agent:dev" || step.MaxAttempts != 1 ||
			len(step.Outputs) != 1 || step.Outputs[0] != output {
			t.Fatalf("step %q is not a single-attempt reference-agent producing %#v", step.Name, output)
		}
	}
}
