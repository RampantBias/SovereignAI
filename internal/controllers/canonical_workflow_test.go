package controllers

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

func TestRuntimeSmokeWorkflowCarriesChangeSetResponsibilities(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "smoke-agent-runtime-workflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow v1alpha1.SovereignWorkflow
	if err := yaml.UnmarshalStrict(data, &workflow); err != nil {
		t.Fatalf("decode runtime smoke workflow: %v", err)
	}
	architectIndex := slices.IndexFunc(workflow.Spec.Steps, func(step v1alpha1.StepConfig) bool { return step.Name == "architect" })
	if architectIndex < 0 || workflow.Spec.Steps[architectIndex].Agent == nil {
		t.Fatal("runtime smoke workflow is missing agent step \"architect\"")
	}
	architectResponsibility := workflow.Spec.Steps[architectIndex].Agent.Responsibility
	for _, expected := range []string{"candidate_write", "affectedPaths", "exact repository-relative path", "Do not use a leading ./", "must correspond directly to an implementation step", "no unrelated file"} {
		if !strings.Contains(architectResponsibility, expected) {
			t.Errorf("architect responsibility does not contain %q", expected)
		}
	}
	for _, stepName := range []string{"test-author", "developer"} {
		index := slices.IndexFunc(workflow.Spec.Steps, func(step v1alpha1.StepConfig) bool { return step.Name == stepName })
		if index < 0 || workflow.Spec.Steps[index].Agent == nil {
			t.Fatalf("runtime smoke workflow is missing agent step %q", stepName)
		}
		responsibility := workflow.Spec.Steps[index].Agent.Responsibility
		for _, expected := range []string{"directly in the attempt workspace"} {
			if !strings.Contains(responsibility, expected) {
				t.Errorf("step %q responsibility does not contain %q", stepName, expected)
			}
		}
	}
}

func outputContracts(references []v1alpha1.ContractReference) string {
	contracts := make([]string, len(references))
	for index, reference := range references {
		contracts[index] = reference.Name + "/" + reference.Version
	}
	slices.Sort(contracts)
	result := ""
	for index, contract := range contracts {
		if index > 0 {
			result += ","
		}
		result += contract
	}
	return result
}

func inputArtifacts(references []v1alpha1.ArtifactReference) string {
	names := make([]string, len(references))
	for index, reference := range references {
		names[index] = reference.Name
	}
	slices.Sort(names)
	result := ""
	for index, name := range names {
		if index > 0 {
			result += ","
		}
		result += name
	}
	return result
}
