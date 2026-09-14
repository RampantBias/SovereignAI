package audit

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func viewAttempt(name, step, kind, execution string, iteration, retry int32, phase v1alpha1.ResourcePhase) v1alpha1.StepAttempt {
	a := v1alpha1.StepAttempt{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "wf", UID: projectionUID(name).UID}, Spec: v1alpha1.StepAttemptSpec{WorkflowRef: projectionUID("wf"), StepName: step, Kind: v1alpha1.ExecutionKind(kind), WorkflowAttempt: iteration, RetryNumber: retry}, Status: v1alpha1.StepAttemptStatus{Phase: phase}}
	if execution != "" {
		executionKind := map[string]string{"Agent": "AgentRun", "Utility": "UtilityOperation", "HumanGate": "ApprovalRequest"}[kind]
		a.Status.ExecutionRef = &v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: executionKind, Name: execution}
	}
	return a
}

func viewWorkflow(t *testing.T) *v1alpha1.SovereignWorkflow {
	t.Helper()
	raw, err := os.ReadFile("../../resources/canonical-workflow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var wf v1alpha1.SovereignWorkflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	wf.Name = "wf"
	wf.Namespace = "wf"
	wf.UID = "wf-uid"
	return &wf
}

func TestWorkflowViewBridgesExistingStoriesWithoutChangingOutcomes(t *testing.T) {
	wf := viewWorkflow(t)
	attempts := []v1alpha1.StepAttempt{
		viewAttempt("merge-attempt", "request-merge", "Utility", "request-merge", 1, 1, v1alpha1.PhaseSucceeded),
		viewAttempt("next-author", "test-author", "Agent", "next-run", 1, 1, v1alpha1.PhaseSucceeded),
		viewAttempt("failed-test", "test-candidate", "Utility", "test", 0, 1, v1alpha1.PhaseFailed),
		viewAttempt("prior-author", "test-author", "Agent", "prior-run", 0, 1, v1alpha1.PhaseSucceeded),
		viewAttempt("review-attempt", "product-approval", "HumanGate", "review", 1, 1, v1alpha1.PhaseSucceeded),
	}
	events := append(recoveryProjectionFixture(t), approvalProjectionFixture(t)...)
	v, err := BuildWorkflowView(wf, attempts, nil, events)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Steps) != 12 || v.Steps[2].Name != "developer" || v.Steps[3].Name != "test-author" || v.Steps[7].Name != "push-candidate" {
		t.Fatal("canonical order changed")
	}
	if len(v.Attempts) != 5 || v.Attempts[0].Resource.Name != "prior-author" || v.Attempts[0].Status.Phase != v1alpha1.PhaseSucceeded || v.Attempts[1].Resource.Name != "failed-test" {
		t.Fatal("rewind history flattened or prior outcome changed")
	}
	if len(v.Attempts[2].StoryIDs) != 1 || len(v.Attempts[4].StoryIDs) != 1 {
		t.Fatal("stories not bridged to replacement and merge")
	}
	for _, s := range v.Stories {
		if s.Status != "linked" {
			t.Fatalf("existing verification changed: %+v", s.Issues)
		}
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	again, err := BuildWorkflowView(wf, attempts, nil, events)
	if err != nil {
		t.Fatal(err)
	}
	v.ExportedAt = time.Time{}
	again.ExportedAt = time.Time{}
	if !reflect.DeepEqual(v, again) {
		t.Fatal("event input order changes display")
	}
}

func TestWorkflowViewGenericFailurePendingApprovalAndScope(t *testing.T) {
	wf := viewWorkflow(t)
	failed := viewAttempt("build-w001-r001", "build-calculator", "Utility", "", 1, 1, v1alpha1.PhaseFailed)
	failed.Status.FailureReason = "BuildRejected"
	failed.Status.FailureMessage = "invalid build context"
	next := viewAttempt("build-w001-r002", "build-calculator", "Utility", "", 1, 2, v1alpha1.PhasePending)
	review := viewAttempt("review-attempt", "product-approval", "HumanGate", "review", 1, 1, v1alpha1.PhaseAwaitingApproval)
	foreign := failed.DeepCopy()
	foreign.UID = "foreign"
	foreign.Spec.WorkflowRef.UID = "other-workflow"
	request := v1alpha1.ApprovalRequest{ObjectMeta: metav1.ObjectMeta{Name: "review", Namespace: "wf", UID: "review-uid"}, Spec: v1alpha1.ApprovalRequestSpec{WorkflowRef: projectionUID("wf"), AttemptRef: projectionUID("review-attempt"), Approval: v1alpha1.ApprovalSpec{RequiredGroups: []string{"maintainers"}}, Inputs: []v1alpha1.ArtifactReference{projectionPin("candidate-revision")}}, Status: v1alpha1.ApprovalRequestStatus{Phase: v1alpha1.PhaseAwaitingApproval}}
	retry := projectionEvent(t, "retry-marker", "StepAttemptRetried", map[string]any{"private": "must not export"})
	retry.Subject.Step = failed.Spec.StepName
	retry.Subject.Attempt = 2
	retry.Target = next.Name
	retry.References = map[string]string{"failedAttempt": failed.Name, "retryAttempt": next.Name}
	wrong := projectionEvent(t, "wrong-inputs", "InputsResolved", InputsResolved{SchemaVersion: "v1", Consumer: projectionRef("ApprovalRequest", "review"), Inputs: []ArtifactEvidence{projectionArtifact(projectionPin("foreign-artifact"))}})
	wrong.Subject.Namespace = "other"
	events := []Event{retry, wrong, approvalProjectionFixture(t)[0]}
	v, err := BuildWorkflowView(wf, []v1alpha1.StepAttempt{failed, next, review, *foreign}, []v1alpha1.ApprovalRequest{request}, events)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Attempts) != 3 || len(v.Stories) != 0 {
		t.Fatal("wrong scope or fabricated story")
	}
	if len(v.Attempts[0].Issues) != 1 || v.Attempts[0].Checkpoints[0].RelatedAttempts["planned_retry"] != next.Name {
		t.Fatal("lost failure visibility or same-step retry")
	}
	if v.Attempts[2].Approval == nil || len(v.Attempts[2].Checkpoints) != 1 || v.Attempts[2].Checkpoints[0].Type != "ApprovalDecisionSubmitted" {
		t.Fatal("pending approval or separate submission missing")
	}
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "must not export") || strings.Contains(string(raw), "foreign-artifact") {
		t.Fatal("unscoped or unknown payload exported")
	}
}

func TestWorkflowViewArtifactAcceptanceAndWrongExecutionUID(t *testing.T) {
	wf := viewWorkflow(t)
	a := viewAttempt("merge-attempt", "request-merge", "Utility", "request-merge", 1, 1, v1alpha1.PhaseSucceeded)
	events := approvalProjectionFixture(t)
	artifact := projectionArtifact(projectionPin("merge-request"))
	artifact.Producer = projectionRef("UtilityOperation", "request-merge")
	events = append(events, projectionEvent(t, "accepted", "ArtifactAccepted", ConsequenceRecorded{SchemaVersion: "v1", DecisionEvent: "authorization", Consequences: []ConsequenceEvidence{{Artifact: &artifact}}}))
	wrong := projectionRef("UtilityOperation", "request-merge")
	wrong.UID = "other-incarnation"
	events = append(events, projectionEvent(t, "wrong", "InputsResolved", InputsResolved{SchemaVersion: "v1", Consumer: wrong}))
	runtime := projectionEvent(t, "runtime", "UtilityOperationCompleted", ConsequenceRecorded{SchemaVersion: "v1", DecisionEvent: "authorization", Consequences: []ConsequenceEvidence{{Kind: "runtime-outcome", After: "succeeded"}}})
	runtime.Subject = Subject{Workflow: "wf", Step: "request-merge", Attempt: 1}
	runtime.References = map[string]string{"utilityOperation": "request-merge"}
	events = append(events, runtime)
	v, err := BuildWorkflowView(wf, []v1alpha1.StepAttempt{a}, nil, events)
	if err != nil {
		t.Fatal(err)
	}
	accepted := false
	completed := false
	for _, c := range v.Attempts[0].Checkpoints {
		if c.EventID == "wrong" {
			t.Fatal("bound evidence by execution name despite wrong UID")
		}
		if c.EventID == "accepted" {
			accepted = true
		}
		if c.EventID == "runtime" {
			completed = true
		}
	}
	if !accepted || !completed {
		t.Fatal("accepted output or decision-linked runtime completion missing")
	}
}
