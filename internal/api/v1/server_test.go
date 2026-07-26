package v1

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SovereignAI/internal/api/requestidentity"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateProjectPersistsIntent(t *testing.T) {
	scheme := testScheme(t)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	recorder := &audit.MemoryRecorder{}
	server := NewServer(client, recorder)
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
	server := NewServer(client, recorder)

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
	server := NewServer(client, recorder)
	requestContext := contractMaintainerContext("frank")
	manifest := `apiVersion: aim.sovereign.io/v1alpha1
kind: SovereignWorkflow
metadata:
  name: change
spec:
  requesterSubject: forged-requester
  requestedVolumeSize: 1Gi
  steps:
  - name: architect
    kind: Agent
    responsibility: plan
    order: 1
`
	response, err := server.CreateWorkflow(requestContext, &pb.CreateWorkflowRequest{
		ProjectName: "platform", ManifestContent: manifest, ChangeRequestContent: validWorkflowChangeRequest(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	var workflow v1alpha1.SovereignWorkflow
	namespace := "platform-" + response.WorkflowId
	if err := client.Get(context.Background(), objectKey(namespace, response.WorkflowId), &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.Spec.Project.Name != "platform" || workflow.Spec.WorkflowID != response.WorkflowId || workflow.Spec.RequesterSubject != "frank" {
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
	for _, event := range recorder.AllEvents() {
		if event.Type == "WorkflowSubmitted" && (event.Requester == nil || event.Requester.ID != "frank") {
			t.Fatalf("workflow submission requester was not recorded: %#v", event)
		}
	}
}

func TestCreateWorkflowRequiresContractMaintainerBeforeSideEffects(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  context.Context
		code codes.Code
	}{
		{name: "missing identity", ctx: context.Background(), code: codes.Unauthenticated},
		{
			name: "wrong group",
			ctx: requestidentity.WithIdentity(context.Background(), requestidentity.Identity{
				Subject: "developer", Groups: []string{"developers"},
			}),
			code: codes.PermissionDenied,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheme := testScheme(t)
			project := &v1alpha1.SovereignProject{}
			project.Name = "platform"
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project).Build()
			server := NewServer(kubeClient, audit.NewMemoryRecorder())

			_, err := server.CreateWorkflow(test.ctx, &pb.CreateWorkflowRequest{
				ProjectName: "platform", ManifestContent: validWorkflowManifest(),
				ChangeRequestContent: validWorkflowChangeRequest(t),
			})
			if grpcstatus.Code(err) != test.code {
				t.Fatalf("CreateWorkflow() code = %s, want %s: %v", grpcstatus.Code(err), test.code, err)
			}
			var namespaces corev1.NamespaceList
			if err := kubeClient.List(context.Background(), &namespaces); err != nil {
				t.Fatal(err)
			}
			if len(namespaces.Items) != 0 {
				t.Fatalf("authorization failure created namespaces: %#v", namespaces.Items)
			}
		})
	}
}

func TestCreateWorkflowRejectsInvalidChangeRequestBeforeSideEffects(t *testing.T) {
	scheme := testScheme(t)
	project := &v1alpha1.SovereignProject{}
	project.Name = "platform"
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project).Build()
	recorder := audit.NewMemoryRecorder()
	server := NewServer(kubeClient, recorder)

	_, err := server.CreateWorkflow(contractMaintainerContext("frank"), &pb.CreateWorkflowRequest{
		ProjectName: "platform", ManifestContent: validWorkflowManifest(),
		ChangeRequestContent: []byte(`{"unexpected":true}`),
	})
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateWorkflow() code = %s, want InvalidArgument: %v", grpcstatus.Code(err), err)
	}
	var namespaces corev1.NamespaceList
	if err := kubeClient.List(context.Background(), &namespaces); err != nil {
		t.Fatal(err)
	}
	if len(namespaces.Items) != 0 {
		t.Fatalf("invalid change request created namespaces: %#v", namespaces.Items)
	}
	if !recorder.Has("WorkflowCreateRejected") {
		t.Fatalf("invalid change request was not audited: %#v", recorder.AllEvents())
	}
}

func TestGetWorkflowTimelineReturnsSortedAuditEvents(t *testing.T) {
	scheme := testScheme(t)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	recorder := audit.NewMemoryRecorder()
	server := NewServer(client, recorder)
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	events := []audit.Event{
		{
			ID:            "b",
			Type:          "StepAttemptStarted",
			SchemaVersion: "v1",
			OccurredAt:    base.Add(time.Minute),
			Actor:         audit.Actor{Kind: "Controller", ID: "stepattempt-controller"},
			Subject:       audit.Subject{Project: "platform", Namespace: "platform-wf-123", Workflow: "wf-123", Step: "developer", Attempt: 1},
			Action:        "start",
			Target:        "developer-001",
			Outcome:       "started",
			CorrelationID: "wf-123",
			References:    map[string]string{"pod": "developer-001"},
			Data:          []byte(`{"phase":"Running"}`),
		},
		{
			ID:            "a",
			Type:          "WorkflowSubmitted",
			SchemaVersion: "v1",
			OccurredAt:    base,
			Actor:         audit.Actor{Kind: "API", ID: "api-server"},
			Subject:       audit.Subject{Project: "platform", Namespace: "platform-wf-123", Workflow: "wf-123"},
			Action:        "submit",
			Target:        "wf-123",
			Outcome:       "accepted",
			CorrelationID: "wf-123",
		},
	}
	for _, event := range events {
		if err := recorder.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	response, err := server.GetWorkflowTimeline(context.Background(), &pb.GetWorkflowTimelineRequest{WorkflowId: "wf-123"})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.GetEvents()) != 2 {
		t.Fatalf("event count = %d, want 2", len(response.GetEvents()))
	}
	if response.GetEvents()[0].GetType() != "WorkflowSubmitted" || response.GetEvents()[1].GetType() != "StepAttemptStarted" {
		t.Fatalf("events not sorted by occurred_at/id: %#v", response.GetEvents())
	}
	started := response.GetEvents()[1]
	if started.GetSubject().GetStep() != "developer" || started.GetSubject().GetAttempt() != 1 {
		t.Fatalf("subject not preserved: %#v", started.GetSubject())
	}
	if started.GetReferences()["pod"] != "developer-001" {
		t.Fatalf("references not preserved: %#v", started.GetReferences())
	}
	if started.GetDataJson() != `{"phase":"Running"}` {
		t.Fatalf("data_json = %q", started.GetDataJson())
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

func contractMaintainerContext(subject string) context.Context {
	return requestidentity.WithIdentity(context.Background(), requestidentity.Identity{
		Subject: subject,
		Groups:  []string{workflowSubmitterGroup},
	})
}

func validWorkflowManifest() string {
	return `apiVersion: aim.sovereign.io/v1alpha1
kind: SovereignWorkflow
metadata:
  name: change
spec:
  requestedVolumeSize: 1Gi
  steps:
  - name: architect
    kind: Agent
    responsibility: plan
    order: 1
`
}

func validWorkflowChangeRequest(t *testing.T) []byte {
	t.Helper()
	content, err := json.Marshal(artifactcontract.ChangeRequest{
		Summary: "Add divide support", Description: "Implement calculator division.",
		AcceptanceCriteria: []string{"84 / 2 returns 42"},
		RepositoryURL:      "https://git.example.test/calculator.git",
		SourceCommit:       strings.Repeat("a", 40),
	})
	if err != nil {
		t.Fatal(err)
	}
	return content
}
