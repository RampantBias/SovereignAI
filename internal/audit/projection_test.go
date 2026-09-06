package audit

import (
	"encoding/json"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"reflect"
	"testing"
	"time"
)

func projectionRef(kind, name string) ResourceRef {
	return ResourceRef{SchemaVersion: "v1", APIVersion: v1alpha1.GroupVersion.String(), Kind: kind, Namespace: "wf", Name: name, UID: name + "-uid"}
}
func projectionUID(name string) v1alpha1.UIDReference {
	return v1alpha1.UIDReference{Name: name, UID: types.UID(name + "-uid")}
}
func projectionEvent(t *testing.T, id, kind string, data any) Event {
	t.Helper()
	e, err := NewEvent(EventOptions{Source: "test", Type: kind, Subject: Subject{Workflow: "wf", Namespace: "wf", Step: "product-approval"}, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	e.ID = id
	return e
}
func projectionPin(name string) v1alpha1.ArtifactReference {
	ref := projectionUID(name)
	return v1alpha1.ArtifactReference{Name: name, ArtifactRef: &ref, Digest: artifactcontract.DigestBytes([]byte(name))}
}
func projectionArtifact(pin v1alpha1.ArtifactReference) ArtifactEvidence {
	return ArtifactEvidence{SchemaVersion: "v1", Artifact: projectionRef("Artifact", pin.ArtifactRef.Name), Contract: pin.Name + "/v1", Digest: pin.Digest, Producer: projectionRef("AgentRun", "producer")}
}
func recoveryProjectionFixture(t *testing.T) []Event {
	prior := projectionUID("prior-author")
	trigger := projectionUID("failed-test")
	next := projectionRef("StepAttempt", "next-author")
	execution := projectionRef("AgentRun", "next-run")
	workflow := projectionRef("SovereignWorkflow", "wf")
	evidence := v1alpha1.WorkflowRecoveryEvidence{TriggerAttempt: trigger, PreviousAttempt: &prior, PreviousOutcome: v1alpha1.PhaseSucceeded, FromWorkflowAttempt: 0, MaxWorkflowAttempt: 1, EvidenceEvents: []string{"failure"}, InputLinks: []v1alpha1.RecoveryInputLink{{Consumer: trigger, Input: projectionPin("prepared"), Producer: projectionUID("prepare")}, {Consumer: projectionUID("prepare"), Input: projectionPin("tests"), Producer: prior}}, SelectedAt: metav1.Now()}
	decision := WorkflowRecoveryDecision{DecisionEvaluated: DecisionEvaluated{SchemaVersion: "v1", Primitive: workflow, Decision: DecisionRef{SchemaVersion: "v1", Outcome: "selected"}}, Action: "RetryWorkflow", RestartStep: "test-author", ToWorkflowAttempt: 1, NextAttemptName: next.Name, Recovery: evidence}
	retry := WorkflowRetryRecorded{SchemaVersion: "v1", Workflow: workflow, DecisionEvent: "selection", RetryOf: &prior, TriggeredBy: trigger, Attempt: next, Execution: execution, WorkflowAttempt: 1, Inputs: []v1alpha1.ArtifactReference{projectionPin("developer-output")}}
	failed := projectionEvent(t, "failure", "StepAttemptFailed", nil)
	failed.Target = trigger.Name
	failed.Outcome = "Failed"
	failed.Reason = "TestRunCodeError"
	result := []Event{projectionEvent(t, "selection", "RecoveryDecisionSelected", decision), projectionEvent(t, "retry", "StepAttemptRetried", retry), failed}
	for _, pair := range [][2]ResourceRef{{projectionRef("StepAttempt", prior.Name), projectionRef("AgentRun", "prior-run")}, {next, execution}} {
		id := DeterministicID("initial-context/v1", pair[1].UID)
		e, err := ContextEvent(ContextSnapshotEvidence{SchemaVersion: "v1", Workflow: workflow, Attempt: pair[0], Execution: pair[1], SnapshotID: id, Format: ContextFormat, Digest: artifactcontract.DigestBytes([]byte("private")), ByteCount: 7, SavedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, e)
	}
	result = append(result, projectionEvent(t, "agent-authority", "ExecutionAuthorityEstablished", AuthorityEstablished{SchemaVersion: "v1", Workflow: workflow, StepAttempt: next, Primitive: execution}), projectionEvent(t, "agent-inputs", "InputsResolved", InputsResolved{SchemaVersion: "v1", Consumer: execution, Inputs: []ArtifactEvidence{projectionArtifact(retry.Inputs[0])}}), projectionEvent(t, "agent-authorization", "AgentExecutionAuthorized", DecisionEvaluated{SchemaVersion: "v1", Primitive: execution, Decision: DecisionRef{SchemaVersion: "v1", Outcome: "allowed"}, InputEvent: "agent-inputs", AuthorityEvent: "agent-authority"}))
	return result
}
func approvalProjectionFixture(t *testing.T) []Event {
	pin := projectionPin("candidate-revision")
	request := projectionRef("ApprovalRequest", "review")
	op := projectionRef("UtilityOperation", "request-merge")
	workflow := projectionRef("SovereignWorkflow", "wf")
	now := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	approved := ApprovalDecisionEvidence{SchemaVersion: "v1", Workflow: projectionUID("wf"), StepAttempt: projectionUID("review-attempt"), Request: projectionUID("review"), Decision: projectionUID("human-decision"), Approver: v1alpha1.Subject{SubjectId: "alice"}, Choice: v1alpha1.Approved, ReviewedInputs: []v1alpha1.ArtifactReference{pin}, AuthoredAt: now, InputEvent: "review-inputs"}
	submitted := projectionEvent(t, "submission", "ApprovalDecisionSubmitted", approved)
	approved.AdmittedAt = &now
	approved.SubmissionEvent = "submission"
	admission := DecisionEvaluated{SchemaVersion: "v1", Primitive: op, Decision: DecisionRef{SchemaVersion: "v1", Outcome: "allowed"}, EvidenceEvents: []string{"approval"}, ApprovalBinding: &v1alpha1.ResolvedApproval{Requirement: v1alpha1.ApprovalRequirement{Step: "product-approval", Subject: pin.Name}, RequestRef: approved.Request, DecisionRef: approved.Decision, Subject: pin, AdmissionEventID: "approval"}}
	auth := DecisionEvaluated{SchemaVersion: "v1", Primitive: op, Decision: DecisionRef{SchemaVersion: "v1", Outcome: "allowed"}, EvidenceEvents: []string{"admission"}, InputEvent: "utility-inputs", AuthorityEvent: "authority"}
	return []Event{submitted, projectionEvent(t, "approval", "ApprovalDecisionAdmitted", approved), projectionEvent(t, "review-inputs", "InputsResolved", InputsResolved{SchemaVersion: "v1", Consumer: request, Inputs: []ArtifactEvidence{projectionArtifact(pin)}}), projectionEvent(t, "admission", "UtilityOperationAdmitted", admission), projectionEvent(t, "authorization", "UtilityExecutionAuthorized", auth), projectionEvent(t, "utility-inputs", "InputsResolved", InputsResolved{SchemaVersion: "v1", Consumer: op, Inputs: []ArtifactEvidence{projectionArtifact(pin)}}), projectionEvent(t, "authority", "ExecutionAuthorityEstablished", AuthorityEstablished{SchemaVersion: "v1", Workflow: workflow, StepAttempt: projectionRef("StepAttempt", "merge-attempt"), Primitive: op})}
}
func TestProjectionLinksBothStoriesAndIsDeterministic(t *testing.T) {
	events := append(recoveryProjectionFixture(t), approvalProjectionFixture(t)...)
	result, err := BuildProjection("wf", "", events)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stories) != 2 {
		t.Fatalf("stories=%d", len(result.Stories))
	}
	refs := map[string]bool{}
	for _, e := range events {
		refs[e.ID] = true
	}
	for _, story := range result.Stories {
		if story.Status != "linked" {
			t.Fatalf("%s: %#v", story.Kind, story.Issues)
		}
		nodes := map[string]LineageNode{}
		for _, node := range story.Nodes {
			nodes[node.ID] = node
		}
		for _, link := range story.Links {
			if !refs[link.EventID] || nodes[link.From].ID == "" || nodes[link.To].ID == "" {
				t.Fatalf("unsupported relation: %#v", link)
			}
		}
		if story.Kind == "recovery" {
			found := false
			for _, link := range story.Links {
				if link.Relation == "retry_of" {
					found = true
					if nodes[link.To].Resource.Name != "prior-author" || nodes[link.To].Outcome != "Succeeded" {
						t.Fatal("misidentified prior author")
					}
				}
			}
			if !found {
				t.Fatal("missing retry relation")
			}
		}
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	replay, err := BuildProjection("wf", "", events)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("projection changes with event order: %v", err)
	}
}
func TestProjectionShowsMissingAndMismatchedEvidence(t *testing.T) {
	for _, scenario := range []string{"missing decision", "different prior UID", "wrong scope", "unknown version", "missing context", "approved digest differs", "missing subject binding", "missing authority", "wrong validation digest"} {
		t.Run(scenario, func(t *testing.T) {
			events := recoveryProjectionFixture(t)
			root := "retry"
			want := "evidence mismatch"
			switch scenario {
			case "missing decision":
				events = events[1:]
				want = "missing evidence"
			case "different prior UID":
				var retry WorkflowRetryRecorded
				json.Unmarshal(events[1].Data, &retry)
				retry.RetryOf.UID = "other"
				events[1].Data, _ = json.Marshal(retry)
			case "wrong scope":
				events[0].Subject.Namespace = "other"
			case "unknown version":
				events[0].SchemaVersion = "v999"
			case "missing context":
				events = events[:3]
				want = "missing evidence"
			default:
				events = approvalProjectionFixture(t)
				root = "approval"
				if scenario == "approved digest differs" {
					var inputs InputsResolved
					json.Unmarshal(events[5].Data, &inputs)
					inputs.Inputs[0].Digest = "sha256:other"
					events[5].Data, _ = json.Marshal(inputs)
				}
				if scenario == "missing subject binding" {
					var admission DecisionEvaluated
					json.Unmarshal(events[3].Data, &admission)
					admission.ApprovalBinding = nil
					events[3].Data, _ = json.Marshal(admission)
					want = "missing evidence"
				}
				if scenario == "missing authority" {
					events = events[:6]
					want = "missing evidence"
				}
				if scenario == "wrong validation digest" {
					var submitted ApprovalDecisionEvidence
					json.Unmarshal(events[0].Data, &submitted)
					submitted.ReviewedInputs[0].Digest = "wrong"
					events[0].Data, _ = json.Marshal(submitted)
				}
			}
			result, err := BuildProjection("wf", root, events)
			if err != nil {
				t.Fatal(err)
			}
			if result.Stories[0].Status != want || len(result.Stories[0].Issues) == 0 {
				t.Fatalf("status: %#v", result.Stories[0])
			}
		})
	}
}
func TestDeniedApprovalDoesNotImplyAuthorization(t *testing.T) {
	events := approvalProjectionFixture(t)[:3]
	for _, i := range []int{0, 1} {
		var data ApprovalDecisionEvidence
		json.Unmarshal(events[i].Data, &data)
		data.Choice = v1alpha1.Denied
		events[i].Data, _ = json.Marshal(data)
	}
	result, err := BuildProjection("wf", "approval", events)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stories[0].Status != "linked" {
		t.Fatal(result.Stories[0].Issues)
	}
	for _, link := range result.Stories[0].Links {
		if link.Relation == "approved_by" || link.Relation == "authorized_by" {
			t.Fatal("denial presented as approval")
		}
	}
}
func TestProjectionRejectsConflictingEventIDs(t *testing.T) {
	events := recoveryProjectionFixture(t)
	duplicate := events[0]
	duplicate.Data = []byte(`{}`)
	events = append(events, duplicate)
	if _, err := BuildProjection("wf", "", events); err == nil {
		t.Fatal("conflicting event IDs accepted")
	}
}

func TestApprovalProjectionLinksReviewedValidationToExactDeployment(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "linked", true: "mismatched candidate"}[mismatch], func(t *testing.T) {
			events := approvalProjectionFixture(t)
			validationPin := projectionPin("validation-result")
			producer := projectionRef("ValidationRun", "validation")
			for _, i := range []int{0, 1} {
				var body ApprovalDecisionEvidence
				json.Unmarshal(events[i].Data, &body)
				body.ReviewedInputs = append(body.ReviewedInputs, validationPin)
				events[i].Data, _ = json.Marshal(body)
			}
			var reviewInputs InputsResolved
			json.Unmarshal(events[2].Data, &reviewInputs)
			artifact := projectionArtifact(validationPin)
			artifact.Producer = producer
			reviewInputs.Inputs = append(reviewInputs.Inputs, artifact)
			events[2].Data, _ = json.Marshal(reviewInputs)
			candidate := projectionArtifact(projectionPin("candidate-revision"))
			if mismatch {
				candidate.Digest = "different"
			}
			evaluated := projectionEvent(t, "validation-event", "ValidationEvaluated", DecisionEvaluated{SchemaVersion: "v1", Primitive: producer, Decision: DecisionRef{SchemaVersion: "v1", Outcome: "allowed"}, InputEvent: "validation-inputs", AuthorityEvent: "validation-authority"})
			evaluated.References = map[string]string{"validationURL": "https://calculator.example.test", "providerRef": "deployment-id"}
			events = append(events, evaluated, projectionEvent(t, "validation-inputs", "InputsResolved", InputsResolved{SchemaVersion: "v1", Consumer: producer, Inputs: []ArtifactEvidence{candidate}}), projectionEvent(t, "validation-authority", "ExecutionAuthorityEstablished", AuthorityEstablished{SchemaVersion: "v1", Workflow: projectionRef("SovereignWorkflow", "wf"), StepAttempt: projectionRef("StepAttempt", "validation-attempt"), Primitive: producer}))
			result, err := BuildProjection("wf", "approval", events)
			if err != nil {
				t.Fatal(err)
			}
			want := "linked"
			if mismatch {
				want = "evidence mismatch"
			}
			if result.Stories[0].Status != want {
				t.Fatal(result.Stories[0].Issues)
			}
			found := false
			for _, node := range result.Stories[0].Nodes {
				if node.Resource != nil && node.Resource.UID == producer.UID {
					found = true
					if node.AccessURL != evaluated.References["validationURL"] || node.ProviderRef != "deployment-id" {
						t.Fatal("missing recorded deployment details")
					}
				}
			}
			if !found {
				t.Fatal("validation producer absent")
			}
		})
	}
}
