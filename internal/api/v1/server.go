package v1

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/SovereignAI/internal/api/requestidentity"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Server implements the generated pb.OrchestratorServiceServer interface
type Server struct {
	pb.UnimplementedOrchestratorServiceServer // Guarantees forward compatibility
	Client                                    client.Client
	Auditor                                   audit.Recorder
}

func NewServer(client client.Client, auditor audit.Recorder) *Server {
	return &Server{
		Client:  client,
		Auditor: auditor,
	}
}

func (s *Server) CleanWorkflow(ctx context.Context, req *pb.CleanWorkflowRequest) (*pb.CleanWorkflowResponse, error) {
	// Get workflow
	var workflows v1alpha1.SovereignWorkflowList
	if err := s.Client.List(ctx, &workflows, client.MatchingLabels{
		"sovereign-ai.io/workflow-id": req.GetWorkflowId(),
		"sovereign-ai.io/project":     req.GetProjectName(),
	}); err != nil {
		if auditErr := s.appendAPIEvent(ctx, "WorkflowCleanupFailed", audit.Subject{Project: req.GetProjectName(), Workflow: req.GetWorkflowId()}, "cleanup", req.GetWorkflowId(), "failed", "WorkflowLookupFailed", req.GetWorkflowId(), nil, map[string]string{"error": err.Error()}); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, status.Errorf(codes.Internal, "failed to locate workflow: %v", err)
	}

	if len(workflows.Items) != 1 {
		if auditErr := s.appendAPIEvent(ctx, "WorkflowCleanupRejected", audit.Subject{Project: req.GetProjectName(), Workflow: req.GetWorkflowId()}, "cleanup", req.GetWorkflowId(), "rejected", "WorkflowNotFound", req.GetWorkflowId(), nil, map[string]any{"matches": len(workflows.Items)}); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, status.Errorf(codes.NotFound, "workflow %q in project %q was not found", req.GetWorkflowId(), req.GetProjectName())
	}

	log.Printf("gRPC network edge: Processing Clean on Workflow %s for Project %s", req.GetWorkflowId(), req.GetProjectName())
	workflow := workflows.Items[0]
	if auditErr := s.appendAPIEvent(ctx, "WorkflowCleanupRequested", audit.Subject{Project: workflow.Spec.Project.Name, Namespace: workflow.Namespace, Workflow: workflow.Name}, "cleanup", workflow.Name, "requested", "", workflow.Name, nil, nil); auditErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: workflow.Namespace}}
	if err := s.Client.Delete(ctx, namespace); err != nil && !apierrors.IsNotFound(err) {
		if auditErr := s.appendAPIEvent(ctx, "WorkflowCleanupFailed", audit.Subject{Project: workflow.Spec.Project.Name, Namespace: workflow.Namespace, Workflow: workflow.Name}, "delete", workflow.Namespace, "failed", "NamespaceDeleteFailed", workflow.Name, nil, map[string]string{"error": err.Error()}); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, status.Errorf(codes.Internal, "failed to delete workflow namespace: %v", err)
	}
	if auditErr := s.appendAPIEvent(ctx, "WorkflowNamespaceDeleteRequested", audit.Subject{Project: workflow.Spec.Project.Name, Namespace: workflow.Namespace, Workflow: workflow.Name}, "delete", workflow.Namespace, "requested", "", workflow.Name, nil, nil); auditErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
	}

	// Success
	return &pb.CleanWorkflowResponse{
		Success: true,
		Message: fmt.Sprintf("Project %s Workflow %s was successfully purged from cluster", req.GetProjectName(), req.GetWorkflowId()),
	}, nil
}

func (s *Server) CreateProject(ctx context.Context, req *pb.CreateProjectRequest) (*pb.CreateProjectResponse, error) {
	name := req.GetProjectName()
	infra := req.GetInfraRepo()
	app := req.GetAppRepo()

	if name == "" || infra == "" || app == "" {
		if auditErr := s.appendAPIEvent(ctx, "ProjectCreateRejected", audit.Subject{Project: name}, "create", name, "rejected", "MissingRequiredField", name, nil, map[string]bool{"hasProjectName": name != "", "hasInfrastructureRepository": infra != "", "hasApplicationRepository": app != ""}); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, status.Error(codes.InvalidArgument, "project name, infrastructure repository, and application repository are required")
	}
	tenant := req.GetExtendedMetadata()["tenant"]
	if tenant == "" {
		tenant = name
	}
	project := &v1alpha1.SovereignProject{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.SovereignProjectSpec{
			Tenant:                tenant,
			ApplicationRepository: v1alpha1.RepositorySpec{URL: app, DefaultRevision: "main"},
			Validation:            v1alpha1.ProjectValidationSpec{Provider: "argocd-kustomize", InfrastructureRepo: infra, OverlayPath: "deployments/overlays/testground"},
			PolicyProfileRef:      "mvp-default",
		},
	}
	if err := s.Client.Create(ctx, project); err != nil {
		if apierrors.IsAlreadyExists(err) {
			if auditErr := s.appendAPIEvent(ctx, "ProjectCreateRejected", audit.Subject{Project: name}, "create", name, "rejected", "AlreadyExists", name, nil, nil); auditErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
			}
			return nil, status.Errorf(codes.AlreadyExists, "project %q already exists", name)
		}
		if auditErr := s.appendAPIEvent(ctx, "ProjectCreateFailed", audit.Subject{Project: name}, "create", name, "failed", "KubernetesCreateFailed", name, nil, map[string]string{"error": err.Error()}); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, status.Errorf(codes.Internal, "failed to create project intent: %v", err)
	}
	if auditErr := s.appendAPIEvent(ctx, "ProjectCreated", audit.Subject{Project: name}, "create", name, "created", "", name, map[string]string{"policyProfile": project.Spec.PolicyProfileRef, "validationProvider": project.Spec.Validation.Provider}, map[string]string{"tenant": tenant, "applicationRepository": app, "infrastructureRepository": infra, "overlayPath": project.Spec.Validation.OverlayPath}); auditErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
	}

	return &pb.CreateProjectResponse{
		Success: true,
		Message: fmt.Sprintf("Project %s declared successfully", name),
	}, nil
}

func (s *Server) ListWorkflows(ctx context.Context, req *pb.ListWorkflowsRequest) (*pb.ListWorkflowsResponse, error) {
	// Get complete list of workflows on system
	var list v1alpha1.SovereignWorkflowList
	options := []client.ListOption{}
	if req.GetProjectName() != "" {
		options = append(options, client.MatchingLabels{"sovereign-ai.io/project": req.GetProjectName()})
	}
	if err := s.Client.List(ctx, &list, options...); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list workflows: %v", err)
	}

	// DTO->pb
	var pbWorkflows []*pb.WorkflowStatusDTO
	for _, wf := range list.Items {
		pbWorkflows = append(pbWorkflows, &pb.WorkflowStatusDTO{
			ProjectName: wf.Spec.Project.Name,
			WorkflowId:  wf.Spec.WorkflowID,
			Status:      wf.Status.Phase,
			CurrentStep: wf.Status.ActiveStepName,
			Namespace:   wf.Namespace,
		})
	}

	return &pb.ListWorkflowsResponse{
		Workflows: pbWorkflows,
	}, nil
}

func (s *Server) GetWorkflowTimeline(ctx context.Context, req *pb.GetWorkflowTimelineRequest) (*pb.GetWorkflowTimelineResponse, error) {
	workflowID := strings.TrimSpace(req.GetWorkflowId())
	if workflowID == "" {
		return nil, status.Error(codes.InvalidArgument, "workflow_id is required")
	}
	if s.Auditor == nil {
		return nil, status.Error(codes.FailedPrecondition, "audit recorder is not configured")
	}

	events, err := s.Auditor.ListWorkflow(ctx, workflowID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to read workflow timeline: %v", err)
	}

	timeline := audit.BuildTimeline(events)
	response := &pb.GetWorkflowTimelineResponse{Events: make([]*pb.AuditEventDTO, 0, len(timeline))}
	for _, event := range timeline {
		response.Events = append(response.Events, timelineEventDTO(event))
	}
	return response, nil
}

func (s *Server) CreateWorkflow(ctx context.Context, req *pb.CreateWorkflowRequest) (*pb.CreateWorkflowResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "workflow request is required")
	}

	// Clean given name
	projectName := normalizeName(strings.TrimSpace(req.ProjectName))
	if projectName == "" {
		return nil, status.Error(codes.InvalidArgument, "project name must not be blank")
	}

	// Get requestor identity
	requester, authErr := requireWorkflowRequester(ctx)
	if authErr != nil {
		reason := "RequesterUnauthenticated"
		if status.Code(authErr) == codes.PermissionDenied {
			reason = "RequesterUnauthorized"
		}
		if auditErr := s.appendAPIEvent(ctx, "WorkflowCreateRejected", audit.Subject{Project: projectName},
			"submit", "", "rejected", reason, projectName, nil, nil); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, authErr
	}

	// Verify there is manifest & change-request content
	if len(req.ManifestContent) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "empty workflow manifest given")
	}
	if len(req.ChangeRequestContent) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "empty change request given")
	}

	// Parse workflow request via unmarshal
	var workflowCRD v1alpha1.SovereignWorkflow
	if err := yaml.Unmarshal([]byte(req.ManifestContent), &workflowCRD); err != nil {
		if auditErr := s.appendAPIEvent(ctx, "WorkflowCreateRejected", audit.Subject{Project: projectName}, "submit",
			"", "rejected", "InvalidManifest", projectName, nil, map[string]string{"error": err.Error()}); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse workflow manifest: %v", err)
	}

	// Expand authoring input before admission. Every stored digest below refers
	// to the structured artifact; fully structured requests remain byte-preserving.
	changeRequestContent, err := artifactcontract.PrepareChangeRequest(req.GetChangeRequestContent())
	if err != nil {
		if auditErr := s.appendAPIEvent(ctx, "WorkflowCreateRejected", audit.Subject{Project: projectName},
			"submit", workflowCRD.Name, "rejected", "InvalidChangeRequest", projectName, nil,
			map[string]string{"error": err.Error()}); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, status.Errorf(codes.InvalidArgument, "invalid change-request/v1: %v", err)
	}

	// Verify Project exists and can be queried
	var project v1alpha1.SovereignProject
	if err := s.Client.Get(ctx, types.NamespacedName{Name: projectName}, &project); err != nil {
		if apierrors.IsNotFound(err) {
			if auditErr := s.appendAPIEvent(ctx, "WorkflowCreateRejected", audit.Subject{Project: projectName},
				"submit", workflowCRD.Name, "rejected", "ProjectNotFound", projectName, nil, nil); auditErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
			}
			return nil, status.Errorf(codes.NotFound, "project %q does not exist", projectName)
		}
		if auditErr := s.appendAPIEvent(ctx, "WorkflowCreateFailed", audit.Subject{Project: projectName},
			"submit", workflowCRD.Name, "failed", "ProjectReadFailed", projectName, nil,
			map[string]string{"error": err.Error()}); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, status.Errorf(codes.Internal, "failed to read project: %v", err)
	}
	if !v1alpha1.ProjectReady(&project) {
		if auditErr := s.appendAPIEvent(ctx, "WorkflowCreateRejected", audit.Subject{Project: projectName},
			"submit", workflowCRD.Name, "rejected", "ProjectNotReady", projectName, nil, nil); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, status.Error(codes.FailedPrecondition, "project must have current ConfigurationValid and ValidationProviderReady conditions before workflow submission")
	}
	// Verify workflow has at least 1 step
	if len(workflowCRD.Spec.Steps) == 0 {
		if auditErr := s.appendAPIEvent(ctx, "WorkflowCreateRejected", audit.Subject{Project: projectName},
			"submit", workflowCRD.Name, "rejected", "InvalidDefinition", projectName, nil,
			map[string]string{"error": "workflow must contain at least one step"}); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, status.Error(codes.InvalidArgument, "workflow must contain at least one step")
	}
	// normalize workflow and namespace names
	baseName := normalizeName(workflowCRD.Name)
	if baseName == "" {
		baseName = "wf"
	}
	workflowID := fmt.Sprintf("%s-%s", baseName, generateRandomSuffix())
	targetNamespace := fmt.Sprintf("%s-%s", projectName, workflowID)

	// Record workflow submission
	subject := audit.Subject{Project: projectName, Namespace: targetNamespace, Workflow: workflowID}
	if auditErr := s.appendAPIEvent(ctx, "WorkflowSubmitted", subject, "submit", workflowID, "accepted", "", workflowID, nil, map[string]any{"baseName": baseName, "stepCount": len(workflowCRD.Spec.Steps), "classification": workflowCRD.Spec.Classification, "definitionRevision": workflowCRD.Spec.DefinitionRevision}); auditErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
	}

	// Dynamic Provisioning of namespace for isolation boundaries
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: targetNamespace,
			Labels: map[string]string{
				"sovereign-ai.io/project":      projectName,
				"app.kubernetes.io/managed-by": "sovereign-orchestrator",
				"sovereign-ai.io/workflow-id":  workflowID,
				//"istio-injection":              "enabled", // Safe hooks for upcoming mesh logic
			},
		},
	}

	// Create namespace
	if err := s.Client.Create(ctx, ns); err != nil {
		if auditErr := s.appendAPIEvent(ctx, "WorkflowCreateFailed", subject, "create", targetNamespace, "failed",
			"NamespaceCreateFailed", workflowID, nil, map[string]string{"error": err.Error()}); auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
		}
		return nil, status.Errorf(codes.Internal, "failed to seed workspace runtime context: %v", err)
	}
	if auditErr := s.appendAPIEvent(ctx, "NamespaceCreated", subject, "create", targetNamespace, "created", "",
		workflowID, map[string]string{"namespace": targetNamespace}, nil); auditErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
	}

	// Stage the exact admitted bytes in an immutable, namespace-local ingress
	// object. The workflow controller will copy this into workspace before a
	// new stepAttempt. This avoids separate handling for the workspace provisioning.
	immutable := true
	const bootstrapKey = "change-request.json"
	changeRequestCm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bootstrapIngressName(workflowID),
			Namespace: targetNamespace,
			Labels: map[string]string{
				"sovereign-ai.io/workflow-id": workflowID,
				"sovereign-ai.io/purpose":     "bootstrap-input",
			},
		},
		Immutable:  &immutable,
		BinaryData: map[string][]byte{bootstrapKey: append([]byte(nil), changeRequestContent...)},
	}
	if err := s.Client.Create(ctx, changeRequestCm); err != nil {
		auditErr := s.appendAPIEvent(ctx, "WorkflowCreateFailed", subject, "create", changeRequestCm.Name, "failed",
			"BootstrapConfigMapCreateFailed", workflowID, nil, map[string]string{"error": err.Error()})
		if rollbackErr := s.rollbackWorkflowNamespace(ctx, ns, subject, workflowID, "BootstrapConfigMapCreateFailed"); rollbackErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to create bootstrap input: %v; rollback failed: %v", err, rollbackErr)
		}
		if auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to create bootstrap input: %v; failed to record audit event: %v", err, auditErr)
		}
		return nil, status.Errorf(codes.Internal, "failed to create bootstrap input: %v", err)
	}
	changeRequestDigest := artifactcontract.DigestBytes(changeRequestContent)
	if auditErr := s.appendAPIEvent(ctx, "WorkflowBootstrapInputStaged", subject, "create", changeRequestCm.Name, "created", "",
		workflowID, map[string]string{"configMap": changeRequestCm.Name, "digest": changeRequestDigest}, nil); auditErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
	}

	// Must override for now
	workflowCRD.ObjectMeta.Name = workflowID
	workflowCRD.ObjectMeta.Namespace = targetNamespace
	if workflowCRD.ObjectMeta.Labels == nil {
		workflowCRD.ObjectMeta.Labels = make(map[string]string)
	}
	workflowCRD.ObjectMeta.Labels["sovereign-ai.io/project"] = projectName
	workflowCRD.ObjectMeta.Labels["sovereign-ai.io/workflow-id"] = workflowID
	workflowCRD.Spec.Project = v1alpha1.UIDReference{Name: projectName, UID: project.UID}
	workflowCRD.Spec.WorkflowID = workflowID
	workflowCRD.Spec.RequesterSubject = requester.Subject
	workflowCRD.Spec.Bootstrap = v1alpha1.WorkflowBootstrapSpec{
		SourceRef:      v1alpha1.UIDReference{Name: changeRequestCm.Name, UID: changeRequestCm.UID},
		Key:            bootstrapKey,
		ExpectedDigest: changeRequestDigest,
		Contract:       v1alpha1.ContractReference{Name: "change-request", Version: "v1"},
		ArtifactName:   "change-request",
	}

	// Create Workflow
	if err := s.Client.Create(ctx, &workflowCRD); err != nil {
		auditErr := s.appendAPIEvent(ctx, "WorkflowCreateFailed", subject, "create", workflowID, "failed",
			"WorkflowCreateFailed", workflowID, nil, map[string]string{"error": err.Error()})
		if rollbackErr := s.rollbackWorkflowNamespace(ctx, ns, subject, workflowID, "WorkflowCreateFailed"); rollbackErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to create workflow: %v; rollback failed: %v", err, rollbackErr)
		}
		if auditErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to create workflow: %v; failed to record audit event: %v", err, auditErr)
		}
		return nil, status.Errorf(codes.Internal, "failed to bind custom resource spec to target substrate: %v", err)
	}
	if auditErr := s.appendAPIEvent(ctx, "WorkflowBootstrapStaged", subject, "create", workflowID, "created", "",
		workflowID, map[string]string{"namespace": targetNamespace, "configMap": changeRequestCm.Name, "digest": changeRequestDigest}, nil); auditErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to record audit event: %v", auditErr)
	}

	// Return parameters back down the wire
	return &pb.CreateWorkflowResponse{
		WorkflowId: workflowID,
	}, nil
}

// Generates a 'random' string for unique Workflow naming
func generateRandomSuffix() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	seededRand := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 5)
	for i := range b {
		b[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(b)
}

func bootstrapIngressName(workflowID string) string {
	const suffix = "-bootstrap-input"
	maximumPrefix := 63 - len(suffix)
	prefix := strings.Trim(workflowID, "-")
	if len(prefix) > maximumPrefix {
		prefix = strings.TrimRight(prefix[:maximumPrefix], "-")
	}
	return prefix + suffix
}

func (s *Server) rollbackWorkflowNamespace(ctx context.Context, namespace *corev1.Namespace, subject audit.Subject, workflowID, reason string) error {
	deleteErr := s.Client.Delete(ctx, namespace)
	outcome := "requested"
	data := map[string]string{}
	if deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
		outcome = "failed"
		data["error"] = deleteErr.Error()
	}
	if auditErr := s.appendAPIEvent(ctx, "NamespaceRollbackRequested", subject, "delete", namespace.Name,
		outcome, reason, workflowID, map[string]string{"namespace": namespace.Name}, data); auditErr != nil {
		return auditErr
	}
	if deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
		return deleteErr
	}
	return nil
}

// Helper method for building API events consistently within the API server
func (s *Server) appendAPIEvent(ctx context.Context, eventType string, subject audit.Subject, action, target, outcome, reason, correlationID string, references map[string]string, data any) error {
	var requester *audit.Actor
	if identity, ok := requestidentity.FromContext(ctx); ok && identity.Valid() {
		requester = &audit.Actor{Kind: "User", ID: identity.Subject}
	}
	return audit.AppendEvent(ctx, s.Auditor, audit.EventOptions{
		Source:        "api",
		Type:          eventType,
		OccurredAt:    time.Now().UTC(),
		Actor:         audit.Actor{Kind: "API", ID: "api-server"},
		Requester:     requester,
		Subject:       subject,
		Action:        action,
		Target:        target,
		Outcome:       outcome,
		Reason:        reason,
		CorrelationID: correlationID,
		References:    references,
		Data:          data,
	})
}

const workflowSubmitterGroup = "contract-maintainers"

func requireWorkflowRequester(ctx context.Context) (requestidentity.Identity, error) {
	identity, ok := requestidentity.FromContext(ctx)
	if !ok || !identity.Valid() {
		return requestidentity.Identity{}, status.Error(codes.Unauthenticated, "authenticated requester is required")
	}
	if !identity.InGroup(workflowSubmitterGroup) {
		return requestidentity.Identity{}, status.Errorf(codes.PermissionDenied, "requester must belong to %q", workflowSubmitterGroup)
	}
	return identity, nil
}

func timelineEventDTO(event audit.TimelineEvent) *pb.AuditEventDTO {
	return &pb.AuditEventDTO{
		Id:            event.ID,
		Type:          event.Type,
		SchemaVersion: event.SchemaVersion,
		OccurredAt:    formatAuditTime(event.OccurredAt),
		RecordedAt:    formatAuditTime(event.RecordedAt),
		Actor: &pb.AuditActorDTO{
			Kind: event.Actor.Kind,
			Id:   event.Actor.ID,
		},
		Subject: &pb.AuditSubjectDTO{
			Project:   event.Subject.Project,
			Namespace: event.Subject.Namespace,
			Workflow:  event.Subject.Workflow,
			Step:      event.Subject.Step,
			Attempt:   event.Subject.Attempt,
		},
		Action:        event.Action,
		Target:        event.Target,
		Outcome:       event.Outcome,
		Reason:        event.Reason,
		CorrelationId: event.CorrelationID,
		CausationId:   event.CausationID,
		DecisionId:    event.DecisionID,
		References:    event.References,
		DataJson:      string(event.Data),
	}
}

func formatAuditTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func normalizeName(value string) string {
	return strings.ToLower(strings.ReplaceAll(value, "_", "-"))
}
