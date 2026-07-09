package v1

import (
	"context"
	"testing"

	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateProjectPersistsIntent(t *testing.T) {
	scheme := testScheme(t)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	recorder := &audit.MemoryRecorder{}
	server := NewServer(client, nil, nil, recorder)
	response, err := server.CreateProject(context.Background(), &pb.CreateProjectRequest{
		ProjectName: "platform", InfraRepo: "ssh://infra", AppRepo: "ssh://app",
		ExtendedMetadata: map[string]string{"tenant": "engineering"},
	})
	if err != nil || !response.Success {
		t.Fatalf("CreateProject() = %#v, %v", response, err)
	}
	var project v1alpha1.SovereignProject
	if err := client.Get(context.Background(), objectKey("", "platform"), &project); err != nil {
		t.Fatal(err)
	}
	if project.Spec.Tenant != "engineering" || project.Spec.Validation.Provider != "argocd-kustomize" {
		t.Fatalf("unexpected project: %#v", project.Spec)
	}
	if !recorder.Has("ProjectCreated") {
		t.Fatalf("expected ProjectCreated audit event, got %#v", recorder.AllEvents())
	}
}

func TestCreateProjectAuditsRejectedRequest(t *testing.T) {
	scheme := testScheme(t)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	recorder := &audit.MemoryRecorder{}
	server := NewServer(client, nil, nil, recorder)

	_, err := server.CreateProject(context.Background(), &pb.CreateProjectRequest{ProjectName: "platform"})
	if err == nil {
		t.Fatal("CreateProject() error = nil, want validation error")
	}
	if !recorder.Has("ProjectCreateRejected") {
		t.Fatalf("expected ProjectCreateRejected audit event, got %#v", recorder.AllEvents())
	}
}

func TestCreateWorkflowCreatesIsolationNamespace(t *testing.T) {
	scheme := testScheme(t)
	project := &v1alpha1.SovereignProject{}
	project.Name = "platform"
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project).Build()
	recorder := &audit.MemoryRecorder{}
	server := NewServer(client, nil, nil, recorder)
	manifest := `apiVersion: aim.sovereign.io/v1alpha1
kind: SovereignWorkflow
metadata:
  name: change
spec:
  requestedVolumeSize: 1Gi
  steps:
  - name: architect
    kind: Agent
    goal: plan
    order: 1
`
	response, err := server.CreateWorkflow(context.Background(), &pb.CreateWorkflowRequest{ProjectName: "platform", ManifestContent: manifest})
	if err != nil {
		t.Fatal(err)
	}
	var workflow v1alpha1.SovereignWorkflow
	namespace := "platform-" + response.WorkflowId
	if err := client.Get(context.Background(), objectKey(namespace, response.WorkflowId), &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.Spec.ProjectName != "platform" || workflow.Spec.WorkflowID != response.WorkflowId {
		t.Fatalf("unexpected workflow identity: %#v", workflow.Spec)
	}
	var ns corev1.Namespace
	if err := client.Get(context.Background(), objectKey("", namespace), &ns); err != nil {
		t.Fatal(err)
	}
	if ns.Labels["sovereign-ai.io/workflow-id"] != response.WorkflowId {
		t.Fatalf("namespace labels = %#v", ns.Labels)
	}
	for _, eventType := range []string{"WorkflowSubmitted", "NamespaceCreated", "WorkflowAdmitted"} {
		if !recorder.Has(eventType) {
			t.Fatalf("expected %s audit event, got %#v", eventType, recorder.AllEvents())
		}
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func objectKey(namespace, name string) clientObjectKey {
	return clientObjectKey{Namespace: namespace, Name: name}
}

type clientObjectKey = struct {
	Namespace string
	Name      string
}
