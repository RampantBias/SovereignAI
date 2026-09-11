package utilityoperation

import (
	"context"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/policy"
	"github.com/SovereignAI/internal/utility"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCandidatePushAdmissionUsesAcceptedBranch(t *testing.T) {
	ctx := context.Background()
	evaluator, err := policy.NewMVP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{
		"accepted", "parameter override", "missing credential", "missing input", "duplicate input",
		"unpinned", "wrong uid", "wrong digest", "wrong producer", "wrong workflow", "pending", "rejected",
		"stale acceptance", "missing claims", "empty branch", "wrong repository", "wrong version",
		"legacy explicit branch", "legacy missing branch",
	} {
		t.Run(scenario, func(t *testing.T) {
			workflow, project := workflowFixture(), projectFixture()
			project.Spec.ApplicationRepository.CredentialRef = v1alpha1.NamespacedReference{Name: "git", Namespace: "credentials"}
			pin := v1alpha1.ArtifactReference{Name: "candidate-revision", Digest: "sha256:" + strings.Repeat("a", 64), ProducerAttemptRef: "commit-001", ArtifactRef: &v1alpha1.UIDReference{Name: "candidate", UID: "candidate-uid"}}
			candidate := &v1alpha1.Artifact{
				ObjectMeta: metav1.ObjectMeta{Name: "candidate", Namespace: "wf", UID: "candidate-uid"},
				Spec: v1alpha1.ArtifactSpec{
					WorkflowRef: workflowRef(workflow), Digest: pin.Digest,
					ProducerRef: v1alpha1.TypedLocalReference{Name: "commit-001"},
					Contract:    v1alpha1.ContractReference{Name: "candidate-revision", Version: "v1"},
					Claims: &v1alpha1.ArtifactClaims{CandidateRevision: &v1alpha1.CandidateRevisionClaims{
						RepositoryURL: project.Spec.ApplicationRepository.URL, Branch: "candidate/wf", Commit: strings.Repeat("b", 40), Tree: strings.Repeat("c", 40),
					}},
				},
				Status: v1alpha1.ArtifactStatus{Phase: v1alpha1.PhaseSucceeded, Conditions: []metav1.Condition{{Type: "Valid", Status: metav1.ConditionTrue}}},
			}
			operation := &v1alpha1.UtilityOperation{
				ObjectMeta: metav1.ObjectMeta{Name: "push-001", Namespace: "wf"},
				Spec: v1alpha1.UtilityOperationSpec{
					WorkflowRef: workflowRef(workflow), StepName: "push", Attempt: 1,
					Operation:       v1alpha1.UtilityOperationRequest{Name: utility.OperationGitPush},
					Inputs:          []v1alpha1.ArtifactReference{pin},
					OutputContracts: []v1alpha1.ContractReference{{Name: "candidate-remote-proof", Version: "v1"}},
				},
			}
			switch scenario {
			case "parameter override":
				operation.Spec.Operation.Parameters = map[string]string{"branch": "unrelated"}
			case "missing credential":
				project.Spec.ApplicationRepository.CredentialRef.Name = ""
			case "missing input":
				operation.Spec.Inputs = nil
			case "duplicate input":
				operation.Spec.Inputs = append(operation.Spec.Inputs, pin)
			case "unpinned":
				operation.Spec.Inputs[0].ArtifactRef = nil
			case "wrong uid":
				candidate.UID = "replaced"
			case "wrong digest":
				candidate.Spec.Digest = "sha256:" + strings.Repeat("d", 64)
			case "wrong producer":
				candidate.Spec.ProducerRef.Name = "other"
			case "wrong workflow":
				candidate.Spec.WorkflowRef.UID = "other"
			case "pending":
				candidate.Status.Phase = v1alpha1.PhasePending
			case "rejected":
				candidate.Status.Phase = v1alpha1.PhaseFailed
			case "stale acceptance":
				candidate.Generation = 2
			case "missing claims":
				candidate.Spec.Claims = nil
			case "empty branch":
				candidate.Spec.Claims.CandidateRevision.Branch = ""
			case "wrong repository":
				candidate.Spec.Claims.CandidateRevision.RepositoryURL = "https://example.test/other.git"
			case "wrong version":
				candidate.Spec.Contract.Version = "v2"
			case "legacy explicit branch", "legacy missing branch":
				operation.Spec.OutputContracts = nil
				operation.Spec.Inputs = nil
				if scenario == "legacy explicit branch" {
					operation.Spec.Operation.Parameters = map[string]string{"branch": "legacy"}
				}
			}
			original := operation.DeepCopy()
			kube := fake.NewClientBuilder().WithScheme(attemptScheme(t)).WithObjects(workflow, project, candidate).Build()
			capture := &pushPolicyCapture{Evaluator: evaluator}
			reconciler := &UtilityOperationReconciler{Client: kube, Policy: capture}
			allowed, _, _, err := reconciler.admit(ctx, operation)
			wantAllowed := scenario == "accepted" || scenario == "parameter override" || scenario == "legacy explicit branch"
			if wantAllowed && (err != nil || !allowed) {
				t.Fatalf("expected admission: allowed=%v err=%v", allowed, err)
			}
			if !wantAllowed && allowed {
				t.Fatal("invalid candidate push was admitted")
			}
			if scenario == "accepted" || scenario == "parameter override" {
				if capture.branch != "candidate/wf" {
					t.Fatalf("policy evaluated branch %q instead of accepted candidate", capture.branch)
				}
			}
			if operation.Spec.Operation.Parameters["branch"] != original.Spec.Operation.Parameters["branch"] {
				t.Fatal("admission mutated immutable operation parameters")
			}
		})
	}
}

type pushPolicyCapture struct {
	policy.Evaluator
	branch string
}

func (p *pushPolicyCapture) Evaluate(ctx context.Context, input any) (policy.Decision, error) {
	request := input.(map[string]any)["request"].(map[string]any)
	p.branch = request["parameters"].(map[string]string)["branch"]
	return p.Evaluator.Evaluate(ctx, input)
}
