package utilityoperation

import (
	"context"
	"encoding/json"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/policy"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"strings"
	"testing"
)

func TestUtilityPolicyConsumesExactHumanApproval(t *testing.T) {
	for _, mutation := range []string{"valid", "missing binding", "missing declaration", "different candidate", "different digest", "old workflow attempt", "replaced decision", "denied", "unadmitted", "different event", "newer gate retry"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			workflow := workflowFixture()
			workflow.Status.Phase = "Running"
			pin := v1alpha1.ArtifactReference{Name: "candidate-revision", Digest: "sha256:" + strings.Repeat("a", 64), ArtifactRef: &v1alpha1.UIDReference{Name: "candidate", UID: "candidate-uid"}}
			workflow.Spec.Steps = []v1alpha1.StepConfig{
				{Name: "gate", Kind: v1alpha1.ExecutionKindHumanGate, Order: 1, Inputs: []v1alpha1.ArtifactReference{pin}},
				{Name: "request", Kind: v1alpha1.ExecutionKindUtility, Order: 2, Utility: &v1alpha1.UtilityOperationRequest{Name: "git.mergeRequest"}, Inputs: []v1alpha1.ArtifactReference{pin}, RequiresApproval: &v1alpha1.ApprovalRequirement{Step: "gate", Subject: "candidate-revision"}},
			}
			gate := authorizedAttempt("gate-1", "wf", "gate", v1alpha1.ExecutionKindHumanGate)
			gate.Spec.WorkflowAttempt = 0
			gate.Status.Phase = v1alpha1.PhaseSucceeded
			gate.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(workflow, v1alpha1.GroupVersion.WithKind("SovereignWorkflow"))}
			consumer := authorizedAttempt("request-1", "wf", "request", v1alpha1.ExecutionKindUtility)
			consumer.Spec.WorkflowAttempt = 0
			consumer.OwnerReferences = gate.OwnerReferences
			workflow.Status.ActiveAttemptRef = consumer.Name
			workflow.Status.ActiveStepName = "request"
			now := metav1.Now()
			request := &v1alpha1.ApprovalRequest{ObjectMeta: metav1.ObjectMeta{Name: gate.Name, Namespace: "wf", UID: "approval-uid", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(gate, v1alpha1.GroupVersion.WithKind("StepAttempt"))}},
				Spec:   v1alpha1.ApprovalRequestSpec{WorkflowRef: workflowRef(workflow), AttemptRef: v1alpha1.UIDReference{Name: gate.Name, UID: gate.UID}, StepName: "gate", Attempt: 1, Inputs: []v1alpha1.ArtifactReference{pin}, Approval: v1alpha1.ApprovalSpec{Mode: v1alpha1.AnyOf, DenyBehavior: "Fail", RequiredGroups: []string{"maintainers"}}},
				Status: v1alpha1.ApprovalRequestStatus{Phase: v1alpha1.PhaseSucceeded, CompletedAt: &now, DecisionRef: controllermeta.ApprovalDecisionName("approval-uid"), DecisionUID: "decision-uid"}}
			decision := &v1alpha1.ApprovalDecision{ObjectMeta: metav1.ObjectMeta{Name: request.Status.DecisionRef, Namespace: "wf", UID: request.Status.DecisionUID, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(request, v1alpha1.GroupVersion.WithKind("ApprovalRequest"))}},
				Spec: v1alpha1.ApprovalDecisionSpec{WorkflowRef: request.Spec.WorkflowRef, StepAttemptRef: request.Spec.AttemptRef, ApprovalRequestRef: v1alpha1.UIDReference{Name: request.Name, UID: request.UID}, Decision: v1alpha1.Approved, AuthoredAt: &now, Subject: v1alpha1.Subject{SubjectId: "alice", Groups: []string{"maintainers"}}}}
			candidate := &v1alpha1.Artifact{ObjectMeta: metav1.ObjectMeta{Name: "candidate", Namespace: "wf", UID: "candidate-uid"},
				Spec:   v1alpha1.ArtifactSpec{WorkflowRef: workflowRef(workflow), Digest: pin.Digest, Contract: v1alpha1.ContractReference{Name: "candidate-revision", Version: "v1"}, Claims: &v1alpha1.ArtifactClaims{CandidateRevision: &v1alpha1.CandidateRevisionClaims{RepositoryURL: "https://github.com/example/app.git", Branch: "demo", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}}},
				Status: v1alpha1.ArtifactStatus{Phase: v1alpha1.PhaseSucceeded, Conditions: []metav1.Condition{{Type: "Valid", Status: metav1.ConditionTrue}}}}
			project := projectFixture()
			project.Spec.ApplicationRepository.CredentialRef = v1alpha1.NamespacedReference{Name: "repo", Namespace: "credentials"}
			kube := fake.NewClientBuilder().WithScheme(attemptScheme(t)).WithObjects(workflow, gate, consumer, request, decision, candidate, project).Build()
			recorder := audit.NewMemoryRecorder()
			binding, err := controllers.ResolveRequiredApproval(ctx, kube, recorder, workflow, consumer, workflow.Spec.Steps[1], []v1alpha1.ArtifactReference{pin})
			if err != nil {
				t.Fatal(err)
			}
			operation := &v1alpha1.UtilityOperation{ObjectMeta: metav1.ObjectMeta{Name: consumer.Name, Namespace: "wf", UID: "operation-uid"}, Spec: v1alpha1.UtilityOperationSpec{WorkflowRef: workflowRef(workflow), AttemptRef: consumer.Name, StepName: "request", Attempt: 1, Operation: *workflow.Spec.Steps[1].Utility, Inputs: []v1alpha1.ArtifactReference{*pin.DeepCopy()}, Approval: binding}}
			switch mutation {
			case "missing binding":
				operation.Spec.Approval = nil
			case "missing declaration":
				workflow.Spec.Steps[1].RequiresApproval = nil
				if err := kube.Update(ctx, workflow); err != nil {
					t.Fatal(err)
				}
			case "different candidate":
				operation.Spec.Inputs[0].ArtifactRef.UID = "other"
			case "different digest":
				operation.Spec.Inputs[0].Digest = "sha256:" + strings.Repeat("b", 64)
			case "different event":
				operation.Spec.Approval.AdmissionEventID = "fabricated"
			case "old workflow attempt":
				gate.Spec.WorkflowAttempt = 1
				if err := kube.Update(ctx, gate); err != nil {
					t.Fatal(err)
				}
			case "replaced decision":
				decision.UID = "replacement"
				if err := kube.Update(ctx, decision); err != nil {
					t.Fatal(err)
				}
			case "denied":
				decision.Spec.Decision = v1alpha1.Denied
				if err := kube.Update(ctx, decision); err != nil {
					t.Fatal(err)
				}
			case "unadmitted":
				request.Status.Phase = v1alpha1.PhaseAwaitingApproval
				if err := kube.Update(ctx, request); err != nil {
					t.Fatal(err)
				}
			case "newer gate retry":
				later := gate.DeepCopy()
				later.Name = "gate-2"
				later.UID = "gate-2-uid"
				later.ResourceVersion = ""
				later.Spec.RetryNumber = 2
				later.Status.Phase = v1alpha1.PhasePending
				if err := kube.Create(ctx, later); err != nil {
					t.Fatal(err)
				}
			}
			evaluator, err := policy.NewMVP(ctx)
			if err != nil {
				t.Fatal(err)
			}
			r := &UtilityOperationReconciler{Client: kube, Policy: evaluator, Audit: recorder}
			allowed, id, reason, err := r.admit(ctx, operation)
			if mutation == "missing binding" {
				// A previously populated policy ID must not bypass a missing
				// binding (including a binding pruned by an outdated CRD).
				operation.Status.PolicyDecisionID = "previous-policy"
				stopped, _, checkErr := r.ensureValidOperation(ctx, operation)
				if !stopped || checkErr == nil {
					t.Fatal("cached policy bypassed missing approval")
				}
			}
			if mutation != "valid" {
				if err == nil && allowed {
					t.Fatal("invalid binding authorized")
				}
				return
			}
			if err != nil || !allowed {
				t.Fatalf("admission=%v %v", allowed, err)
			}
			payload, err := utilityAdmissionDecision(operation, id, allowed, reason)
			if err != nil {
				t.Fatal(err)
			}
			if len(payload.EvidenceEvents) != 1 || payload.EvidenceEvents[0] != binding.AdmissionEventID {
				t.Fatal("policy evidence lost human admission link")
			}
			events := recorder.AllEvents()
			if len(events) != 2 {
				t.Fatalf("expected deduplicated submission/admission, got %d", len(events))
			}
			for _, event := range events {
				if event.ID == binding.AdmissionEventID {
					var admitted audit.ApprovalDecisionEvidence
					if err := json.Unmarshal(event.Data, &admitted); err != nil {
						t.Fatal(err)
					}
					if admitted.Decision.UID != binding.DecisionRef.UID || admitted.ReviewedInputs[0].ArtifactRef.UID != pin.ArtifactRef.UID {
						t.Fatal("policy subject differs from human evidence")
					}
				}
			}
		})
	}
}
