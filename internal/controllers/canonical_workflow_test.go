package controllers

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/utility"
	"sigs.k8s.io/yaml"
)

func TestCanonicalWorkflowMatchesPlan4Spine(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "canonical-workflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow v1alpha1.SovereignWorkflow
	if err := yaml.UnmarshalStrict(data, &workflow); err != nil {
		t.Fatalf("decode canonical workflow: %v", err)
	}

	wantNames := []string{
		"initialize-repository", "architect", "test-author", "developer",
		"prepare-candidate", "test-candidate", "commit-candidate",
		"push-candidate", "build-calculator", "ephemeral-validation",
		"product-approval", "merge-candidate",
	}
	wantKinds := []v1alpha1.ExecutionKind{
		v1alpha1.ExecutionKindUtility, v1alpha1.ExecutionKindAgent,
		v1alpha1.ExecutionKindAgent, v1alpha1.ExecutionKindAgent,
		v1alpha1.ExecutionKindUtility, v1alpha1.ExecutionKindUtility,
		v1alpha1.ExecutionKindUtility, v1alpha1.ExecutionKindUtility,
		v1alpha1.ExecutionKindUtility, v1alpha1.ExecutionKindValidation,
		v1alpha1.ExecutionKindHumanGate, v1alpha1.ExecutionKindUtility,
	}
	wantOperations := map[int]string{
		0:  utility.OperationRepositoryInitialize,
		4:  utility.OperationCandidatePrepare,
		5:  utility.OperationTestRun,
		6:  utility.OperationGitCommit,
		7:  utility.OperationGitPush,
		8:  utility.OperationBuildImage,
		11: utility.OperationGitMerge,
	}
	wantOutputs := []string{
		"repository-revision/v1",
		"implementation-plan/v1",
		"test-change-set/v1",
		"change-set/v1",
		"prepared-candidate/v1",
		"test-report/v1",
		"candidate-revision/v1",
		"candidate-remote-proof/v1",
		"image-digest/v1",
		"validation-result/v1",
		"",
		"merge-revision/v1",
	}
	wantInputs := []string{
		"change-request",
		"change-request,repository-revision",
		"change-request,implementation-plan,repository-revision",
		"change-request,implementation-plan,repository-revision,test-change-set",
		"change-set,repository-revision,test-change-set",
		"prepared-candidate",
		"prepared-candidate,test-report",
		"candidate-revision",
		"candidate-remote-proof,candidate-revision",
		"candidate-remote-proof,candidate-revision,image-digest",
		"candidate-remote-proof,candidate-revision,image-digest,validation-result",
		"candidate-remote-proof,candidate-revision,validation-result",
	}

	if workflow.Spec.DefinitionRevision != "plan4-v1" {
		t.Fatalf("definitionRevision = %q, want plan4-v1", workflow.Spec.DefinitionRevision)
	}
	if workflow.Spec.Project.Name == "" || workflow.Spec.Project.UID == "" {
		t.Fatalf("Project reference must contain name and UID: %#v", workflow.Spec.Project)
	}
	if len(workflow.Spec.Steps) != len(wantNames) {
		t.Fatalf("step count = %d, want %d", len(workflow.Spec.Steps), len(wantNames))
	}

	for index, step := range workflow.Spec.Steps {
		if step.Order != index+1 || step.Name != wantNames[index] || step.Kind != wantKinds[index] {
			t.Fatalf("step %d = order %d, name %q, kind %q", index+1, step.Order, step.Name, step.Kind)
		}
		if operation, ok := wantOperations[index]; ok {
			if step.Utility == nil || step.Utility.Name != operation {
				t.Fatalf("step %q operation = %#v, want %q", step.Name, step.Utility, operation)
			}
		}
		if got := outputContracts(step.Outputs); got != wantOutputs[index] {
			t.Fatalf("step %q outputs = %q, want %q", step.Name, got, wantOutputs[index])
		}
		if got := inputArtifacts(step.Inputs); got != wantInputs[index] {
			t.Fatalf("step %q inputs = %q, want %q", step.Name, got, wantInputs[index])
		}
	}

	for _, index := range []int{1, 2, 3} {
		agent := workflow.Spec.Steps[index].Agent
		if agent == nil || agent.Inference == nil {
			t.Fatalf("step %q must be an inference-backed Agent", workflow.Spec.Steps[index].Name)
		}
		if agent.Inference.Model != "code-small" ||
			agent.Inference.SharingScope != v1alpha1.SharingWithinWorkflow {
			t.Fatalf("step %q has a different model request", workflow.Spec.Steps[index].Name)
		}
		if index >= 2 {
			for _, expected := range []string{"patchLines", "one physical Git unified-diff line per item", "diff --git", "exact repository paths and contents"} {
				if !strings.Contains(agent.Responsibility, expected) {
					t.Errorf("step %q responsibility does not contain %q", workflow.Spec.Steps[index].Name, expected)
				}
			}
		}
	}
	architectResponsibility := workflow.Spec.Steps[1].Agent.Responsibility
	for _, expected := range []string{"affectedPaths", "exact repository-relative path", "Do not use a leading ./", "must correspond directly to an implementation step", "no unrelated file"} {
		if !strings.Contains(architectResponsibility, expected) {
			t.Errorf("architect responsibility does not contain %q", expected)
		}
	}
	if responsibility := workflow.Spec.Steps[2].Agent.Responsibility; !strings.Contains(responsibility, "recognized test paths") || !strings.Contains(responsibility, "never modify production code") {
		t.Errorf("test-author responsibility does not carry test-only authority: %q", responsibility)
	}
	developerResponsibility := workflow.Spec.Steps[3].Agent.Responsibility
	for _, expected := range []string{"production-code changes", "accepted test change set", "exactly match the repository path manifest", "never select a recognized test path", "smallest hunks"} {
		if !strings.Contains(developerResponsibility, expected) {
			t.Errorf("developer responsibility does not contain %q", expected)
		}
	}
	if provider := workflow.Spec.Steps[9].Validation; provider == nil || provider.Provider != supportedProjectValidationProvider {
		t.Fatalf("validation must use %q", supportedProjectValidationProvider)
	}
}

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
	for _, expected := range []string{"affectedPaths", "exact repository-relative path", "Do not use a leading ./", "must correspond directly to an implementation step", "no unrelated file"} {
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
		for _, expected := range []string{"patchLines", "one physical Git unified-diff line per item", "diff --git", "exact repository paths and contents"} {
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
