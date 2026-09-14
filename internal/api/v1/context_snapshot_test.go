package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/requestidentity"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/inference"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
	"time"
)

type snapshotTestStore struct {
	*audit.MemoryRecorder
	saved       map[string]audit.ContextSnapshot
	unavailable bool
}

func (s *snapshotTestStore) SaveContext(ctx context.Context, value audit.ContextSnapshot) (audit.ContextSnapshotEvidence, error) {
	if s.unavailable {
		return audit.ContextSnapshotEvidence{}, errors.New("offline")
	}
	if err := audit.ValidateSnapshot(value); err != nil {
		return audit.ContextSnapshotEvidence{}, err
	}
	if old, ok := s.saved[value.Evidence.SnapshotID]; ok {
		if old.Evidence.Digest != value.Evidence.Digest {
			return audit.ContextSnapshotEvidence{}, audit.ErrSnapshotConflict
		}
		return old.Evidence, nil
	}
	event, err := audit.ContextEvent(value.Evidence)
	if err != nil {
		return audit.ContextSnapshotEvidence{}, err
	}
	if err := s.Append(ctx, event); err != nil {
		return audit.ContextSnapshotEvidence{}, err
	}
	s.saved[value.Evidence.SnapshotID] = value
	return value.Evidence, nil
}
func (s *snapshotTestStore) GetContext(_ context.Context, id string) (audit.ContextSnapshot, error) {
	value, ok := s.saved[id]
	if !ok {
		return value, audit.ErrSnapshotNotFound
	}
	return value, audit.ValidateSnapshot(value)
}
func snapshotAPIFixture(t *testing.T) (*Server, *pb.StoreContextSnapshotRequest, context.Context, *snapshotTestStore) {
	t.Helper()
	schema := testScheme(t)
	workflow := &v1alpha1.SovereignWorkflow{ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: "wf", UID: "wf-uid"}, Status: v1alpha1.SovereignWorkflowStatus{Phase: "Running", ActiveAttemptRef: "author"}}
	ref := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}
	attempt := &v1alpha1.StepAttempt{ObjectMeta: metav1.ObjectMeta{Name: "author", Namespace: "wf", UID: "attempt-uid", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(workflow, v1alpha1.GroupVersion.WithKind("SovereignWorkflow"))}}, Spec: v1alpha1.StepAttemptSpec{WorkflowRef: ref, StepName: "test-author", Kind: v1alpha1.ExecutionKindAgent, RetryNumber: 1}, Status: v1alpha1.StepAttemptStatus{ExecutionRef: &v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: "author"}}}
	content := []byte(`{"private":"repository content"}`)
	artifact := &v1alpha1.Artifact{ObjectMeta: metav1.ObjectMeta{Name: "input", Namespace: "wf", UID: "input-uid"}, Spec: v1alpha1.ArtifactSpec{WorkflowRef: ref, Contract: v1alpha1.ContractReference{Name: "change-request", Version: "v1"}, Digest: artifactcontract.DigestBytes(content)}, Status: v1alpha1.ArtifactStatus{Phase: v1alpha1.PhaseSucceeded, Conditions: []metav1.Condition{{Type: "Valid", Status: metav1.ConditionTrue}}}}
	pin := v1alpha1.ArtifactReference{Name: "change-request", Digest: artifact.Spec.Digest, ArtifactRef: &v1alpha1.UIDReference{Name: artifact.Name, UID: artifact.UID}}
	run := &v1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "author", Namespace: "wf", UID: "run-uid", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(attempt, v1alpha1.GroupVersion.WithKind("StepAttempt"))}}, Spec: v1alpha1.AgentRunSpec{WorkflowRef: ref, AttemptRef: attempt.Name, StepName: attempt.Spec.StepName, Attempt: 1, Inputs: []v1alpha1.ArtifactReference{pin}, PriorAttemptRef: &v1alpha1.FailedAgentAttempt{PreviousAttemptRef: "test", Code: "TestRunCodeError", Message: "stale test"}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: agentcontract.ContextCredentialName(run.Name), Namespace: "wf", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(run, v1alpha1.GroupVersion.WithKind("AgentRun"))}}, Data: map[string][]byte{"token": []byte("run-token")}}
	store := &snapshotTestStore{MemoryRecorder: audit.NewMemoryRecorder(), saved: map[string]audit.ContextSnapshot{}}
	kube := fake.NewClientBuilder().WithScheme(schema).WithObjects(workflow, attempt, run, artifact, secret).Build()
	body := audit.InitialContext{SchemaVersion: audit.ContextFormat, Request: inference.ChatRequest{Model: "demo", Messages: []inference.Message{{Role: "system", Content: "private prompt"}}}, Artifacts: []audit.ContextArtifact{{Metadata: agentcontract.ArtifactInput{Name: pin.Name, Contract: "change-request/v1", Digest: pin.Digest}, Content: string(content)}}, RetryFeedback: &agentcontract.RetryFeedback{PreviousAttemptRef: "test", Code: "TestRunCodeError", Message: "stale test"}}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.StoreContextSnapshotRequest{Namespace: "wf", AgentRun: "author", AgentRunUid: string(run.UID), Snapshot: raw}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-sovereign-context-token", "run-token"))
	return NewServer(kube, store), req, ctx, store
}
func TestContextSnapshotIsPrivateIdempotentAndSurvivesWorkflowCleanup(t *testing.T) {
	server, req, ctx, store := snapshotAPIFixture(t)
	first, err := server.StoreContextSnapshot(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.StoreContextSnapshot(ctx, req)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("duplicate upload: %v", err)
	}
	events := store.AllEvents()
	if len(events) != 1 {
		t.Fatalf("events=%d", len(events))
	}
	data, _ := json.Marshal(events)
	if bytes.Contains(data, []byte("private prompt")) || bytes.Contains(data, []byte("repository content")) || bytes.Contains(data, []byte("run-token")) {
		t.Fatal("raw context leaked into audit")
	}
	// The read path only needs persistent storage; Kubernetes history can disappear.
	for _, obj := range []client.Object{&v1alpha1.AgentRun{}, &v1alpha1.StepAttempt{}, &v1alpha1.SovereignWorkflow{}} {
		obj.SetNamespace("wf")
		obj.SetName("author")
		if _, ok := obj.(*v1alpha1.SovereignWorkflow); ok {
			obj.SetName("wf")
		}
		if err := server.Client.Delete(context.Background(), obj); err != nil {
			t.Fatal(err)
		}
	}
	human := requestidentity.WithIdentity(context.Background(), requestidentity.Identity{Subject: "operator", Groups: []string{workflowSubmitterGroup}})
	result, err := server.GetContextSnapshot(human, &pb.GetContextSnapshotRequest{Id: first.Id})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(req.Snapshot, result.Snapshot) || result.Receipt.Digest != artifactcontract.DigestBytes(result.Snapshot) {
		t.Fatal("retrieved bytes differ")
	}
	if _, err := server.GetContextSnapshot(ctx, &pb.GetContextSnapshotRequest{Id: first.Id}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("runtime credential retrieved private context: %v", err)
	}
}
func TestContextUploadRejectsIdentityContentAndPersistenceFailures(t *testing.T) {
	for _, scenario := range []string{"wrong token", "wrong UID", "changed artifact", "changed feedback", "storage unavailable", "changed initial context"} {
		t.Run(scenario, func(t *testing.T) {
			server, req, ctx, store := snapshotAPIFixture(t)
			want := codes.InvalidArgument
			switch scenario {
			case "wrong token":
				ctx = metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-sovereign-context-token", "other"))
				want = codes.Unauthenticated
			case "wrong UID":
				req.AgentRunUid = "recreated"
				want = codes.Unauthenticated
			case "storage unavailable":
				store.unavailable = true
				want = codes.Unavailable
			case "changed initial context":
				if _, err := server.StoreContextSnapshot(ctx, req); err != nil {
					t.Fatal(err)
				}
				req.Snapshot = bytes.ReplaceAll(req.Snapshot, []byte("private prompt"), []byte("different prompt"))
				want = codes.AlreadyExists
			case "changed artifact":
				req.Snapshot = bytes.ReplaceAll(req.Snapshot, []byte("repository content"), []byte("changed content"))
			case "changed feedback":
				req.Snapshot = bytes.ReplaceAll(req.Snapshot, []byte("stale test"), []byte("different failure"))
			}
			if _, err := server.StoreContextSnapshot(ctx, req); status.Code(err) != want {
				t.Fatalf("got %v want %v", err, want)
			}
			if scenario != "changed initial context" && len(store.AllEvents()) != 0 {
				t.Fatal("failed save reported saved")
			}
		})
	}
}
func TestContextUploadInterceptorDoesNotBypassHumanAuthForReads(t *testing.T) {
	humanCalls := 0
	interceptor := ContextUploadInterceptor(func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
		humanCalls++
		return nil, status.Error(codes.Unauthenticated, "human auth")
	})
	handler := func(context.Context, any) (any, error) { return "upload-handler", nil }
	if _, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: pb.OrchestratorService_StoreContextSnapshot_FullMethodName}, handler); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{pb.OrchestratorService_GetContextSnapshot_FullMethodName, pb.OrchestratorService_GetWorkflowLineage_FullMethodName, pb.OrchestratorService_SubmitApproval_FullMethodName} {
		if _, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method}, handler); status.Code(err) != codes.Unauthenticated {
			t.Fatal("bypassed human auth")
		}
	}
	if humanCalls != 3 {
		t.Fatal("wrong authentication dispatch")
	}
}

func TestLineageAPIUsesPersistedEventsAndRequiresHumanAccess(t *testing.T) {
	server, _, _, store := snapshotAPIFixture(t)
	now := time.Now().UTC()
	payload := audit.ApprovalDecisionEvidence{SchemaVersion: "v1", Workflow: v1alpha1.UIDReference{Name: "wf", UID: "wf-uid"}, Request: v1alpha1.UIDReference{Name: "review", UID: "review-uid"}, Decision: v1alpha1.UIDReference{Name: "decision", UID: "decision-uid"}, Choice: v1alpha1.Approved, AdmittedAt: &now}
	raw, _ := json.Marshal(payload)
	if err := store.Append(context.Background(), audit.Event{ID: "approval-root", Type: "ApprovalDecisionAdmitted", SchemaVersion: "v1", Subject: audit.Subject{Namespace: "wf", Workflow: "wf"}, Data: raw}); err != nil {
		t.Fatal(err)
	}
	human := requestidentity.WithIdentity(context.Background(), requestidentity.Identity{Subject: "operator", Groups: []string{workflowSubmitterGroup}})
	result, err := server.GetWorkflowLineage(human, &pb.GetWorkflowLineageRequest{WorkflowId: "wf", EventId: "approval-root"})
	if err != nil {
		t.Fatal(err)
	}
	var projection audit.Projection
	if err := json.Unmarshal([]byte(result.ProjectionJson), &projection); err != nil {
		t.Fatal(err)
	}
	if len(projection.Stories) != 1 || projection.Stories[0].Status != "missing evidence" {
		t.Fatal("API hid missing persisted references")
	}
	if _, err := server.GetWorkflowLineage(context.Background(), &pb.GetWorkflowLineageRequest{WorkflowId: "wf"}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("anonymous projection allowed")
	}
}
