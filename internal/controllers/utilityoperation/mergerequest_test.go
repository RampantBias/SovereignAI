package utilityoperation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/utility"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMergeRequestWorkloadUsesProjectRepositoryAndScopedCredential(t *testing.T) {
	project, workflow := projectFixture(), workflowFixture()
	project.Spec.ApplicationRepository.URL = "https://github.com/example/calculator.git"
	project.Spec.ApplicationRepository.CredentialRef = v1alpha1.NamespacedReference{Namespace: "credentials", Name: "git"}
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "request-001", Namespace: "wf", UID: "request-uid"},
		Spec: v1alpha1.UtilityOperationSpec{
			WorkflowRef: workflowRef(workflow), StepName: "request", Attempt: 1,
			Operation: v1alpha1.UtilityOperationRequest{Name: utility.OperationGitMergeRequest, Parameters: map[string]string{
				"repositoryURL": "https://attacker.invalid/repository.git", "targetBranch": "release",
			}},
			OutputContracts: []v1alpha1.ContractReference{{Name: "merge-request", Version: "v1"}},
		},
		Status: v1alpha1.UtilityOperationStatus{PolicyDecisionID: "policy"},
	}
	grant := controllers.WorkspaceWriterGrant{LeaseName: "lease", HolderIdentity: "holder", Epoch: 1}
	workload, err := buildUtilityWorkloadConfig(operation, workflow, project, "utility:dev", grant)
	if err != nil {
		t.Fatal(err)
	}
	if workload.input.Parameters["repositoryURL"] != project.Spec.ApplicationRepository.URL ||
		workload.input.Parameters["targetBranch"] != "release" ||
		workload.credentialRef != project.Spec.ApplicationRepository.CredentialRef ||
		workload.credentialClass != utility.CredentialClassRepository || len(workload.input.Command) != 0 {
		t.Fatalf("request escaped Project configuration: %#v", workload)
	}
	workload.credentialSecret = "request-001-credential"
	job := buildUtilityJob(operation, "workspace", "input", "utility:dev", workload, grant)
	container := job.Spec.Template.Spec.Containers[0]
	found := false
	for _, env := range container.Env {
		if env.Name == utility.RepositoryCredentialFileEnv {
			found = env.Value == "/var/run/sovereign/credentials/credentials"
		}
	}
	if !found {
		t.Fatal("API credential file was not wired into the utility container")
	}
	found = false
	for _, mount := range container.VolumeMounts {
		if mount.Name == "operation-credential" {
			found = mount.ReadOnly
		}
	}
	if !found {
		t.Fatal("credential must be mounted read-only")
	}
	encoded, err := json.Marshal(workload.input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "attacker.invalid") || strings.Contains(string(encoded), "request-001-credential") {
		t.Fatal("runtime input contains unapproved repository or credential secret")
	}
	if !utility.IsPrivilegedOperation(utility.OperationGitMergeRequest) || !utility.IsSupportedOperation(utility.OperationGitMerge) {
		t.Fatal("request must require policy while preserving the separate merge operation")
	}
}
