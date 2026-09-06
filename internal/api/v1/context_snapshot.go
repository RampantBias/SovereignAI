package v1

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/domain/state"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Only the upload RPC uses an attempt-scoped credential. Every other RPC keeps
// the existing human JWT boundary, including private-context retrieval.
func ContextUploadInterceptor(human grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == pb.OrchestratorService_StoreContextSnapshot_FullMethodName {
			return handler(ctx, req)
		}
		return human(ctx, req, info, handler)
	}
}

func (s *Server) StoreContextSnapshot(ctx context.Context, req *pb.StoreContextSnapshotRequest) (*pb.ContextSnapshotReceipt, error) {
	if req == nil || req.Namespace == "" || req.AgentRun == "" || req.AgentRunUid == "" {
		return nil, status.Error(codes.InvalidArgument, "namespace and AgentRun identity are required")
	}
	var run v1alpha1.AgentRun
	if err := s.Client.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.AgentRun}, &run); err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid context upload identity")
	}
	var secret corev1.Secret
	tokens := metadata.ValueFromIncomingContext(ctx, "x-sovereign-context-token")
	err := s.Client.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: agentcontract.ContextCredentialName(run.Name)}, &secret)
	if err != nil || string(run.UID) != req.AgentRunUid || !metav1.IsControlledBy(&secret, &run) || len(tokens) != 1 || len(secret.Data["token"]) == 0 || subtle.ConstantTimeCompare([]byte(tokens[0]), secret.Data["token"]) != 1 {
		return nil, status.Error(codes.Unauthenticated, "invalid context upload identity")
	}
	store, ok := s.Auditor.(audit.SnapshotStore)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "durable context storage is not configured")
	}
	if !run.DeletionTimestamp.IsZero() || state.IsTerminal(run.Status.Phase) {
		return nil, status.Error(codes.FailedPrecondition, "AgentRun is no longer active")
	}
	valid, err := controllers.ValidateDomainAuthority(ctx, s.Client, &run, run.Spec.AttemptRef, v1alpha1.ExecutionKindAgent, run.Spec.WorkflowRef, run.Spec.StepName, run.Spec.Attempt)
	if err != nil || !valid {
		return nil, status.Error(codes.FailedPrecondition, "AgentRun has no valid execution authority")
	}
	var attempt v1alpha1.StepAttempt
	var workflow v1alpha1.SovereignWorkflow
	if err := s.Client.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.AttemptRef}, &attempt); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "attempt is unavailable")
	}
	if err := s.Client.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef.Name}, &workflow); err != nil || workflow.UID != run.Spec.WorkflowRef.UID || !metav1.IsControlledBy(&attempt, &workflow) || workflow.Status.ActiveAttemptRef != attempt.Name || state.IsTerminal(v1alpha1.ResourcePhase(workflow.Status.Phase)) {
		return nil, status.Error(codes.FailedPrecondition, "workflow does not authorize this attempt")
	}
	if len(req.Snapshot) == 0 || len(req.Snapshot) > audit.MaxContextBytes {
		return nil, status.Error(codes.InvalidArgument, "invalid context snapshot size")
	}
	var body audit.InitialContext
	if err := json.Unmarshal(req.Snapshot, &body); err != nil || body.SchemaVersion != audit.ContextFormat || len(body.Request.Messages) == 0 || len(body.Artifacts) != len(run.Spec.Inputs) {
		return nil, status.Error(codes.InvalidArgument, "invalid initial context snapshot")
	}
	var feedback *agentcontract.RetryFeedback
	if prior := run.Spec.PriorAttemptRef; prior != nil {
		feedback = &agentcontract.RetryFeedback{PreviousAttemptRef: prior.PreviousAttemptRef, Code: prior.Code, Message: prior.Message}
	}
	if !reflect.DeepEqual(feedback, body.RetryFeedback) || (run.Spec.Inference != nil && body.Request.Model != run.Spec.Inference.Model) {
		return nil, status.Error(codes.InvalidArgument, "context does not match the AgentRun")
	}
	for i, pin := range run.Spec.Inputs {
		artifact, ready, reason, err := artifacts.ResolvePinnedInput(ctx, s.Client, run.Namespace, run.Spec.WorkflowRef, pin)
		if err != nil || !ready || reason != "" {
			return nil, status.Error(codes.FailedPrecondition, "context input pin cannot be verified")
		}
		captured := body.Artifacts[i]
		if captured.Metadata.Name != pin.Name || captured.Metadata.Digest != pin.Digest || captured.Metadata.Contract != artifact.Spec.Contract.Name+"/"+artifact.Spec.Contract.Version || artifactcontract.DigestBytes([]byte(captured.Content)) != pin.Digest {
			return nil, status.Error(codes.InvalidArgument, "context input content does not match its pin")
		}
	}
	evidence := audit.ContextSnapshotEvidence{SchemaVersion: audit.PayloadSchemaVersionV1, Workflow: contextResource("SovereignWorkflow", &workflow), Attempt: contextResource("StepAttempt", &attempt), Execution: contextResource("AgentRun", &run),
		SnapshotID: audit.DeterministicID("initial-context/v1", string(run.UID)), Digest: artifactcontract.DigestBytes(req.Snapshot), Format: audit.ContextFormat, ByteCount: len(req.Snapshot), Inputs: run.Spec.Inputs, SavedAt: time.Now().UTC()}
	saved, err := store.SaveContext(ctx, audit.ContextSnapshot{Evidence: evidence, Bytes: req.Snapshot})
	if errors.Is(err, audit.ErrSnapshotConflict) {
		return nil, status.Error(codes.AlreadyExists, "this AgentRun already saved different initial context")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "context persistence is unavailable")
	}
	return contextReceipt(saved), nil
}

func contextResource(kind string, object client.Object) audit.ResourceRef {
	return audit.ResourceRef{SchemaVersion: audit.PayloadSchemaVersionV1, APIVersion: v1alpha1.GroupVersion.String(), Kind: kind, Namespace: object.GetNamespace(), Name: object.GetName(), UID: string(object.GetUID())}
}
func contextReceipt(e audit.ContextSnapshotEvidence) *pb.ContextSnapshotReceipt {
	return &pb.ContextSnapshotReceipt{Id: e.SnapshotID, Digest: e.Digest, EventId: audit.ContextEventID(e.SnapshotID)}
}

func (s *Server) GetContextSnapshot(ctx context.Context, req *pb.GetContextSnapshotRequest) (*pb.GetContextSnapshotResponse, error) {
	if _, err := requireWorkflowRequester(ctx); err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.Id) == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot id is required")
	}
	store, ok := s.Auditor.(audit.SnapshotStore)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "durable context storage is not configured")
	}
	snapshot, err := store.GetContext(ctx, req.Id)
	if errors.Is(err, audit.ErrSnapshotNotFound) {
		return nil, status.Error(codes.NotFound, "snapshot not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "snapshot could not be read or verified")
	}
	return &pb.GetContextSnapshotResponse{Receipt: contextReceipt(snapshot.Evidence), Snapshot: snapshot.Bytes}, nil
}
