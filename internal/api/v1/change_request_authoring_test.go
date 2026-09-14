package v1

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateWorkflowExpandsCalculatorCriteriaBeforeStaging(t *testing.T) {
	project := &v1alpha1.SovereignProject{}
	project.Name = "platform"
	readyProjectForSubmission(project)
	kube := &assignUIDClient{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(project).Build()}
	recorder := audit.NewMemoryRecorder()
	server := NewServer(kube, recorder)
	draft, err := os.ReadFile(filepath.Join("..", "..", "..", "demo", "change-requests", "calculator-divide.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.CreateWorkflow(contractMaintainerContext("frank"), &pb.CreateWorkflowRequest{
		ProjectName:          "platform",
		ManifestContent:      validWorkflowManifest(),
		ChangeRequestContent: draft})
	if err != nil {
		t.Fatal(err)
	}
	namespace := "platform-" + response.WorkflowId
	var workflow v1alpha1.SovereignWorkflow
	if err := kube.Get(context.Background(), objectKey(namespace, response.WorkflowId), &workflow); err != nil {
		t.Fatal(err)
	}
	var source corev1.ConfigMap
	if err := kube.Get(context.Background(), objectKey(namespace, workflow.Spec.Bootstrap.SourceRef.Name), &source); err != nil {
		t.Fatal(err)
	}
	stored := source.BinaryData[workflow.Spec.Bootstrap.Key]
	if string(stored) == string(draft) {
		t.Fatal("unstructured authoring bytes were stored")
	}
	if err := artifactcontract.ValidateContract(artifactcontract.ChangeRequestContract, stored); err != nil {
		t.Fatal(err)
	}
	if workflow.Spec.Bootstrap.ExpectedDigest != artifactcontract.DigestBytes(stored) {
		t.Fatal("bootstrap pins authoring bytes instead of canonical artifact")
	}
	var cr artifactcontract.ChangeRequest
	if err := json.Unmarshal(stored, &cr); err != nil {
		t.Fatal(err)
	}
	if cr.AcceptanceCriteria[0].ID != "RQ-001" || cr.AcceptanceCriteriaSetDigest == "" {
		t.Fatal("canonical criterion identity missing")
	}
	found := false
	for _, event := range recorder.AllEvents() {
		if event.Type == "WorkflowBootstrapInputStaged" {
			found = true
			if !strings.Contains(string(event.Data), workflow.Spec.Bootstrap.ExpectedDigest) && event.References["digest"] != workflow.Spec.Bootstrap.ExpectedDigest {
				t.Fatal("staging event does not identify canonical artifact")
			}
		}
	}
	if !found {
		t.Fatal("staging evidence missing")
	}
}

func TestCreateWorkflowRejectsTamperedCriteriaBeforeSideEffects(t *testing.T) {
	project := &v1alpha1.SovereignProject{}
	project.Name = "platform"
	readyProjectForSubmission(project)
	kube := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(project).Build()
	recorder := audit.NewMemoryRecorder()
	server := NewServer(kube, recorder)
	tampered := strings.Replace(string(validWorkflowChangeRequest(t)), "84 / 2 returns 42", "84 / 2 returns 41", 1)
	_, err := server.CreateWorkflow(contractMaintainerContext("frank"), &pb.CreateWorkflowRequest{
		ProjectName:          "platform",
		ManifestContent:      validWorkflowManifest(),
		ChangeRequestContent: []byte(tampered)})
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected rejected digest: %v", err)
	}
	var namespaces corev1.NamespaceList
	if err := kube.List(context.Background(), &namespaces); err != nil {
		t.Fatal(err)
	}
	if len(namespaces.Items) != 0 || !recorder.Has("WorkflowCreateRejected") {
		t.Fatal("tampered criteria created workflow resources or were not audited")
	}
}
