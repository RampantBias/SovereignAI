package audit

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	"github.com/SovereignAI/internal/api/v1alpha1"
)

// Projection is deliberately limited to the two presentation stories. It contains
// no private context bytes and makes no whole-workflow completeness claim.
type Projection struct {
	SchemaVersion string         `json:"schemaVersion"`
	WorkflowID    string         `json:"workflowId"`
	Scope         string         `json:"scope"`
	Stories       []LineageStory `json:"stories"`
}
type LineageStory struct {
	EventID string         `json:"eventId"`
	Kind    string         `json:"kind"`
	Status  string         `json:"status"`
	Issues  []LineageIssue `json:"issues"`
	Nodes   []LineageNode  `json:"nodes"`
	Links   []LineageLink  `json:"links"`
}
type LineageIssue struct {
	Status  string `json:"status"`
	EventID string `json:"eventId"`
	Reason  string `json:"reason"`
}
type LineageNode struct {
	AccessURL    string                    `json:"accessURL,omitempty"`
	ProviderRef  string                    `json:"providerRef,omitempty"`
	ID           string                    `json:"id"`
	Kind         string                    `json:"kind"`
	Label        string                    `json:"label"`
	Resource     *ResourceRef              `json:"resource,omitempty"`
	Artifact     *ArtifactEvidence         `json:"artifact,omitempty"`
	Outcome      string                    `json:"outcome,omitempty"`
	Summary      string                    `json:"summary,omitempty"`
	Context      *ContextSnapshotEvidence  `json:"context,omitempty"`
	Approval     *ApprovalDecisionEvidence `json:"approval,omitempty"`
	SourceEvents []string                  `json:"sourceEvents"`
}
type LineageLink struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
	EventID  string `json:"eventId"`
}
type storyBuilder struct {
	story    LineageStory
	events   []Event
	byID     map[string]Event
	workflow ResourceRef
}

func BuildProjection(workflowID, selectedEvent string, events []Event) (Projection, error) {
	result := Projection{SchemaVersion: PayloadSchemaVersionV1, WorkflowID: workflowID, Scope: "Selected recovery and approval evidence only", Stories: []LineageStory{}}
	byID := map[string]Event{}
	ordered := append([]Event(nil), events...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for _, e := range ordered {
		if previous, ok := byID[e.ID]; ok && !reflect.DeepEqual(previous, e) {
			return result, fmt.Errorf("conflicting audit event ID %s", e.ID)
		}
		byID[e.ID] = e
	}
	seen := map[string]bool{}
	for _, e := range ordered {
		if seen[e.ID] || e.Subject.Workflow != workflowID || (selectedEvent != "" && e.ID != selectedEvent) {
			continue
		}
		seen[e.ID] = true
		kind := ""
		if e.Type == "StepAttemptRetried" {
			var marker struct {
				DecisionEvent string `json:"decisionEvent"`
			}
			_ = json.Unmarshal(e.Data, &marker)
			if marker.DecisionEvent != "" {
				kind = "recovery"
			}
		} else if e.Type == "ApprovalDecisionAdmitted" {
			kind = "approval"
		}
		if kind == "" {
			continue
		}
		b := storyBuilder{story: LineageStory{EventID: e.ID, Kind: kind, Status: "linked", Issues: []LineageIssue{}, Nodes: []LineageNode{}, Links: []LineageLink{}}, events: ordered, byID: byID}
		if kind == "recovery" {
			b.recovery(e)
		} else {
			b.approval(e)
		}
		sort.Slice(b.story.Nodes, func(i, j int) bool { return b.story.Nodes[i].ID < b.story.Nodes[j].ID })
		sort.Slice(b.story.Links, func(i, j int) bool {
			a, c := b.story.Links[i], b.story.Links[j]
			return a.From+a.Relation+a.To+a.EventID < c.From+c.Relation+c.To+c.EventID
		})
		result.Stories = append(result.Stories, b.story)
	}
	if selectedEvent != "" && len(result.Stories) == 0 {
		return result, fmt.Errorf("selected event is not a supported recovery or approval root in this workflow")
	}
	return result, nil
}
func (b *storyBuilder) issue(status, id, reason string) {
	for _, i := range b.story.Issues {
		if i.Status == status && i.EventID == id && i.Reason == reason {
			return
		}
	}
	b.story.Issues = append(b.story.Issues, LineageIssue{status, id, reason})
	if status == "evidence mismatch" || b.story.Status == "linked" {
		b.story.Status = status
	}
}
func (b *storyBuilder) decode(e Event, target any) bool {
	var version struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if e.ID == "" || e.SchemaVersion != PayloadSchemaVersionV1 || json.Unmarshal(e.Data, &version) != nil || version.SchemaVersion != PayloadSchemaVersionV1 || json.Unmarshal(e.Data, target) != nil {
		b.issue("evidence mismatch", e.ID, "unsupported or malformed event payload")
		return false
	}
	return true
}
func (b *storyBuilder) source(id, kind string, target any) bool {
	e, ok := b.byID[id]
	if !ok || id == "" {
		b.issue("missing evidence", id, "missing "+kind+" event")
		return false
	}
	if e.Type != kind || e.Subject.Workflow != b.workflow.Name || e.Subject.Namespace != b.workflow.Namespace {
		b.issue("evidence mismatch", id, "event type or workflow scope differs")
		return false
	}
	return b.decode(e, target)
}
func sameResource(a, c ResourceRef) bool {
	return a.Kind == c.Kind && a.APIVersion == c.APIVersion && a.Namespace == c.Namespace && a.Name == c.Name && a.UID != "" && a.UID == c.UID
}
func (b *storyBuilder) validResource(ref ResourceRef, kind, event string) bool {
	if ref.SchemaVersion != PayloadSchemaVersionV1 || ref.APIVersion != v1alpha1.GroupVersion.String() || ref.Kind != kind || ref.Namespace != b.workflow.Namespace || ref.Name == "" || ref.UID == "" {
		b.issue("evidence mismatch", event, "invalid "+kind+" identity")
		return false
	}
	return true
}
func (b *storyBuilder) ref(kind string, ref v1alpha1.UIDReference) ResourceRef {
	return ResourceRef{SchemaVersion: PayloadSchemaVersionV1, APIVersion: v1alpha1.GroupVersion.String(), Kind: kind, Namespace: b.workflow.Namespace, Name: ref.Name, UID: string(ref.UID)}
}
func (b *storyBuilder) node(node LineageNode) string {
	for i := range b.story.Nodes {
		old := &b.story.Nodes[i]
		if old.ID == node.ID {
			if old.Resource != nil && node.Resource != nil && !sameResource(*old.Resource, *node.Resource) {
				b.issue("evidence mismatch", node.SourceEvents[0], "conflicting resource identities")
				return ""
			}
			for _, id := range node.SourceEvents {
				if !contains(old.SourceEvents, id) {
					old.SourceEvents = append(old.SourceEvents, id)
					sort.Strings(old.SourceEvents)
				}
			}
			return old.ID
		}
	}
	b.story.Nodes = append(b.story.Nodes, node)
	return node.ID
}
func (b *storyBuilder) resource(ref ResourceRef, outcome, event string) string {
	if !b.validResource(ref, ref.Kind, event) {
		return ""
	}
	return b.node(LineageNode{ID: ref.Kind + ":" + ref.UID, Kind: ref.Kind, Label: ref.Name, Resource: &ref, Outcome: outcome, SourceEvents: []string{event}})
}
func (b *storyBuilder) link(from, to, relation, event string) {
	if from == "" || to == "" {
		return
	}
	value := LineageLink{from, to, relation, event}
	for _, old := range b.story.Links {
		if old == value {
			return
		}
	}
	b.story.Links = append(b.story.Links, value)
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func (b *storyBuilder) pin(pin v1alpha1.ArtifactReference, event string) string {
	if pin.ArtifactRef == nil || pin.Digest == "" || pin.Name == "" {
		b.issue("missing evidence", event, "artifact has no exact pin")
		return ""
	}
	ref := b.ref("Artifact", *pin.ArtifactRef)
	if !b.validResource(ref, "Artifact", event) {
		return ""
	}
	for _, node := range b.story.Nodes {
		if node.ID == "Artifact:"+ref.UID && node.Artifact != nil && (node.Artifact.Digest != pin.Digest || strings.Split(node.Artifact.Contract, "/")[0] != pin.Name) {
			b.issue("evidence mismatch", event, "artifact digest or contract differs")
			return ""
		}
	}
	evidence := ArtifactEvidence{SchemaVersion: PayloadSchemaVersionV1, Artifact: ref, Contract: pin.Name, Digest: pin.Digest}
	return b.node(LineageNode{ID: "Artifact:" + ref.UID, Kind: "Artifact", Label: pin.Name, Artifact: &evidence, Resource: &ref, SourceEvents: []string{event}})
}
func pinMatches(pin v1alpha1.ArtifactReference, e ArtifactEvidence) bool {
	return pin.ArtifactRef != nil && pin.ArtifactRef.Name == e.Artifact.Name && string(pin.ArtifactRef.UID) == e.Artifact.UID && pin.Digest != "" && pin.Digest == e.Digest && pin.Name == strings.Split(e.Contract, "/")[0] && (pin.ProducerAttemptRef == "" || pin.ProducerAttemptRef == e.Producer.Name)
}

func (b *storyBuilder) recovery(event Event) {
	var retry WorkflowRetryRecorded
	if !b.decode(event, &retry) {
		return
	}
	b.workflow = retry.Workflow
	if b.workflow.Name != event.Subject.Workflow || b.workflow.Namespace != event.Subject.Namespace || !b.validResource(b.workflow, "SovereignWorkflow", event.ID) || !b.validResource(retry.Attempt, "StepAttempt", event.ID) || !b.validResource(retry.Execution, "AgentRun", event.ID) {
		b.issue("evidence mismatch", event.ID, "retry scope differs")
		return
	}
	var decision WorkflowRecoveryDecision
	if !b.source(retry.DecisionEvent, "RecoveryDecisionSelected", &decision) {
		return
	}
	if !sameResource(decision.Primitive, retry.Workflow) || decision.Action != "RetryWorkflow" || decision.Decision.SchemaVersion != PayloadSchemaVersionV1 || decision.Decision.Outcome != "selected" || decision.ToWorkflowAttempt != retry.WorkflowAttempt || decision.Recovery.FromWorkflowAttempt+1 != retry.WorkflowAttempt || decision.NextAttemptName != retry.Attempt.Name || decision.Recovery.TriggerAttempt != retry.TriggeredBy || !reflect.DeepEqual(decision.Recovery.PreviousAttempt, retry.RetryOf) || !sameFeedback(decision.Recovery.Feedback, retry.Feedback) {
		b.issue("evidence mismatch", retry.DecisionEvent, "recovery decision and replacement attempt disagree")
		return
	}
	next := b.resource(retry.Attempt, "created", event.ID)
	execution := b.resource(retry.Execution, "created", event.ID)
	b.link(execution, next, "produced_by", event.ID)
	trigger := b.resource(b.ref("StepAttempt", retry.TriggeredBy), "Failed", retry.DecisionEvent)
	decisionID := b.node(LineageNode{ID: "event:" + retry.DecisionEvent, Kind: "RecoveryDecision", Label: decision.RestartStep, Outcome: decision.Action, SourceEvents: []string{retry.DecisionEvent}})
	b.link(next, decisionID, "authorized_by", event.ID)
	b.link(next, trigger, "triggered_by", event.ID)
	if retry.RetryOf == nil {
		b.issue("missing evidence", event.ID, "prior author attempt is unavailable")
	} else {
		priorRef := b.ref("StepAttempt", *retry.RetryOf)
		prior := b.resource(priorRef, string(decision.Recovery.PreviousOutcome), retry.DecisionEvent)
		b.link(next, prior, "retry_of", event.ID)
		b.contextFor(priorRef, nil)
		b.produced(priorRef)
	}
	for _, gap := range decision.Recovery.Missing {
		b.issue("missing evidence", retry.DecisionEvent, gap)
	}
	for _, id := range decision.Recovery.EvidenceEvents {
		source, ok := b.byID[id]
		if !ok {
			b.issue("missing evidence", id, "trigger failure event is unavailable")
			continue
		}
		if source.SchemaVersion != PayloadSchemaVersionV1 || source.Type != "StepAttemptFailed" || source.Subject.Namespace != b.workflow.Namespace || source.Subject.Workflow != b.workflow.Name || source.Target != retry.TriggeredBy.Name {
			b.issue("evidence mismatch", id, "trigger failure event differs")
			continue
		}
		failure := b.node(LineageNode{ID: "event:" + id, Kind: "TestFailure", Label: source.Reason, Outcome: source.Outcome, SourceEvents: []string{id}})
		b.link(trigger, failure, "supported_by", retry.DecisionEvent)
	}
	for _, edge := range decision.Recovery.InputLinks {
		consumer := b.resource(b.ref("StepAttempt", edge.Consumer), "", retry.DecisionEvent)
		producer := b.resource(b.ref("StepAttempt", edge.Producer), "", retry.DecisionEvent)
		artifact := b.pin(edge.Input, retry.DecisionEvent)
		b.link(consumer, artifact, "used_inputs_from", retry.DecisionEvent)
		b.link(artifact, producer, "produced_by", retry.DecisionEvent)
	}
	for _, pin := range decision.Recovery.Reports {
		b.link(trigger, b.pin(pin, retry.DecisionEvent), "supported_by", retry.DecisionEvent)
	}
	for _, pin := range retry.Inputs {
		b.link(next, b.pin(pin, event.ID), "used_inputs_from", event.ID)
	}
	b.contextFor(retry.Attempt, &retry.Execution)
	b.agentAuthorization(retry)
	b.produced(retry.Attempt)
}
func sameFeedback(a, c *v1alpha1.FailedAgentAttempt) bool { return reflect.DeepEqual(a, c) }

func (b *storyBuilder) contextFor(attempt ResourceRef, expectedExecution *ResourceRef) {
	found := false
	for _, e := range b.events {
		if e.Type != "ContextSnapshotSaved" || e.Subject.Workflow != b.workflow.Name || e.Subject.Namespace != b.workflow.Namespace {
			continue
		}
		var snapshot ContextSnapshotEvidence
		if json.Unmarshal(e.Data, &snapshot) != nil || snapshot.Attempt.UID != attempt.UID {
			continue
		}
		found = true
		if !b.decode(e, &snapshot) {
			continue
		}
		if !sameResource(snapshot.Workflow, b.workflow) || !sameResource(snapshot.Attempt, attempt) || (expectedExecution != nil && !sameResource(snapshot.Execution, *expectedExecution)) || !b.validResource(snapshot.Execution, "AgentRun", e.ID) || snapshot.Format != ContextFormat || snapshot.SnapshotID != DeterministicID("initial-context/v1", snapshot.Execution.UID) || snapshot.Digest == "" || snapshot.ByteCount < 1 || e.ID != ContextEventID(snapshot.SnapshotID) {
			b.issue("evidence mismatch", e.ID, "context receipt identity differs")
			continue
		}
		id := b.node(LineageNode{ID: "context:" + snapshot.SnapshotID, Kind: "SavedContext", Label: "Initial context saved", Context: &snapshot, SourceEvents: []string{e.ID}})
		b.link(b.resource(attempt, "", e.ID), id, "supported_by", e.ID)
	}
	if !found {
		b.issue("missing evidence", b.story.EventID, "initial context receipt missing for "+attempt.Name)
	}
}
func (b *storyBuilder) produced(attempt ResourceRef) {
	for _, e := range b.events {
		if e.Type != "ArtifactAccepted" || e.Subject.Workflow != b.workflow.Name || e.Subject.Namespace != b.workflow.Namespace {
			continue
		}
		var consequence ConsequenceRecorded
		if json.Unmarshal(e.Data, &consequence) != nil {
			continue
		}
		for _, c := range consequence.Consequences {
			a := c.Artifact
			if a == nil || a.ProducerGrant == nil || a.ProducerGrant.UID != attempt.UID {
				continue
			}
			if !b.decode(e, &consequence) || !sameResource(*a.ProducerGrant, attempt) || !b.validResource(a.Artifact, "Artifact", e.ID) {
				b.issue("evidence mismatch", e.ID, "artifact producer differs")
				continue
			}
			pin := v1alpha1.ArtifactReference{Name: strings.Split(a.Contract, "/")[0], Digest: a.Digest, ArtifactRef: &v1alpha1.UIDReference{Name: a.Artifact.Name, UID: types.UID(a.Artifact.UID)}}
			id := b.pin(pin, e.ID)
			for i := range b.story.Nodes {
				if b.story.Nodes[i].ID == id {
					b.story.Nodes[i].Outcome = "accepted"
					copy := *a
					b.story.Nodes[i].Artifact = &copy
				}
			}
			b.link(id, b.resource(attempt, "", e.ID), "produced_by", e.ID)
		}
	}
}

func (b *storyBuilder) approval(event Event) {
	var approval ApprovalDecisionEvidence
	if !b.decode(event, &approval) {
		return
	}
	b.workflow = ResourceRef{SchemaVersion: PayloadSchemaVersionV1, APIVersion: v1alpha1.GroupVersion.String(), Kind: "SovereignWorkflow", Namespace: event.Subject.Namespace, Name: approval.Workflow.Name, UID: string(approval.Workflow.UID)}
	if b.workflow.Name != event.Subject.Workflow || !b.validResource(b.workflow, "SovereignWorkflow", event.ID) || approval.AdmittedAt == nil {
		b.issue("evidence mismatch", event.ID, "approval has no admitted workflow identity")
		return
	}
	request := b.ref("ApprovalRequest", approval.Request)
	decision := b.ref("ApprovalDecision", approval.Decision)
	if !b.validResource(request, "ApprovalRequest", event.ID) || !b.validResource(decision, "ApprovalDecision", event.ID) {
		return
	}
	root := b.resource(decision, string(approval.Choice), event.ID)
	for i := range b.story.Nodes {
		if b.story.Nodes[i].ID == root {
			b.story.Nodes[i].Approval = &approval
		}
	}
	subject := b.resource(request, "reviewed", event.ID)
	if approval.Choice == v1alpha1.Approved {
		b.link(subject, root, "approved_by", event.ID)
	} else if approval.Choice == v1alpha1.Denied {
		b.link(subject, root, "supported_by", event.ID)
	} else {
		b.issue("evidence mismatch", event.ID, "unknown human decision")
		return
	}
	var submitted ApprovalDecisionEvidence
	if b.source(approval.SubmissionEvent, "ApprovalDecisionSubmitted", &submitted) {
		expected := approval
		expected.AdmittedAt = nil
		expected.SubmissionEvent = ""
		if !reflect.DeepEqual(submitted, expected) {
			b.issue("evidence mismatch", approval.SubmissionEvent, "submitted and admitted human decisions differ")
		} else {
			submission := b.node(LineageNode{ID: "event:" + approval.SubmissionEvent, Kind: "HumanSubmission", Label: approval.Approver.SubjectId, Outcome: string(approval.Choice), SourceEvents: []string{approval.SubmissionEvent}})
			b.link(root, submission, "supported_by", event.ID)
		}
	}
	b.inputs(approval.InputEvent, request, approval.ReviewedInputs, subject)
	authorized := false
	for _, e := range b.events {
		if e.Type != "UtilityOperationAdmitted" || e.Subject.Workflow != b.workflow.Name || e.Subject.Namespace != b.workflow.Namespace {
			continue
		}
		var admission DecisionEvaluated
		if json.Unmarshal(e.Data, &admission) != nil || !contains(admission.EvidenceEvents, event.ID) {
			continue
		}
		if !b.decode(e, &admission) || admission.Decision.SchemaVersion != PayloadSchemaVersionV1 || admission.Decision.Outcome != "allowed" || !b.validResource(admission.Primitive, "UtilityOperation", e.ID) || approval.Choice != v1alpha1.Approved {
			b.issue("evidence mismatch", e.ID, "utility admission does not match approved intent")
			continue
		}
		binding := admission.ApprovalBinding
		if binding == nil {
			b.issue("missing evidence", e.ID, "explicit approved-subject binding is missing")
			continue
		}
		reviewed := false
		for _, pin := range approval.ReviewedInputs {
			if reflect.DeepEqual(pin, binding.Subject) {
				reviewed = true
			}
		}
		if binding.RequestRef != approval.Request || binding.DecisionRef != approval.Decision || binding.AdmissionEventID != event.ID || binding.Requirement.Subject != binding.Subject.Name || binding.Requirement.Step != event.Subject.Step || !reviewed {
			b.issue("evidence mismatch", e.ID, "declared approval binding differs from admitted subject")
			continue
		}
		op := b.resource(admission.Primitive, "admitted", e.ID)
		b.link(op, root, "approved_by", e.ID)
		for _, authEvent := range b.events {
			if authEvent.Type != "UtilityExecutionAuthorized" || authEvent.Subject.Workflow != b.workflow.Name || authEvent.Subject.Namespace != b.workflow.Namespace {
				continue
			}
			var authorization DecisionEvaluated
			if json.Unmarshal(authEvent.Data, &authorization) != nil || !contains(authorization.EvidenceEvents, e.ID) {
				continue
			}
			if !b.decode(authEvent, &authorization) || !sameResource(authorization.Primitive, admission.Primitive) || authorization.Decision.SchemaVersion != PayloadSchemaVersionV1 || authorization.Decision.Outcome != "allowed" {
				b.issue("evidence mismatch", authEvent.ID, "utility authorization identity differs")
				continue
			}
			var inputs InputsResolved
			if b.source(authorization.InputEvent, "InputsResolved", &inputs) {
				if !sameResource(inputs.Consumer, admission.Primitive) {
					b.issue("evidence mismatch", authorization.InputEvent, "authorized input consumer differs")
					continue
				}
				shared := false
				for _, pin := range []v1alpha1.ArtifactReference{binding.Subject} {
					for _, input := range inputs.Inputs {
						if input.Artifact.UID == "" || pin.ArtifactRef == nil || (input.Artifact.UID != string(pin.ArtifactRef.UID) && strings.Split(input.Contract, "/")[0] != pin.Name) {
							continue
						}
						if !pinMatches(pin, input) {
							b.issue("evidence mismatch", authorization.InputEvent, "reviewed and authorized artifact pins differ")
							continue
						}
						shared = true
						b.link(op, b.pin(pin, authorization.InputEvent), "used_inputs_from", authorization.InputEvent)
					}
				}
				if !shared {
					b.issue("missing evidence", authorization.InputEvent, "no exact reviewed subject appears in authorized inputs")
					continue
				}
				authorized = true
				auth := b.node(LineageNode{ID: "event:" + authEvent.ID, Kind: "UtilityAuthorization", Label: admission.Primitive.Name, Outcome: authorization.Decision.Outcome, SourceEvents: []string{authEvent.ID}})
				b.link(op, auth, "authorized_by", authEvent.ID)
				b.authority(authorization.AuthorityEvent, admission.Primitive, auth)
			}
		}
	}
	if approval.Choice == v1alpha1.Approved && !authorized {
		b.issue("missing evidence", event.ID, "follow-on utility authorization has not been recorded")
	}
}
func (b *storyBuilder) inputs(id string, consumer ResourceRef, pins []v1alpha1.ArtifactReference, node string) {
	var inputs InputsResolved
	if !b.source(id, "InputsResolved", &inputs) {
		return
	}
	if !sameResource(inputs.Consumer, consumer) || len(inputs.Inputs) != len(pins) {
		b.issue("evidence mismatch", id, "reviewed input set or consumer differs")
		return
	}
	for _, pin := range pins {
		found := false
		for _, input := range inputs.Inputs {
			if pinMatches(pin, input) && b.validResource(input.Artifact, "Artifact", id) {
				found = true
				artifactNode := b.pin(pin, id)
				b.link(node, artifactNode, "used_inputs_from", id)
				if input.Producer.Kind == "ValidationRun" {
					b.validation(input, artifactNode)
				}
			}
		}
		if !found {
			b.issue("evidence mismatch", id, "reviewed artifact identity or digest differs")
		}
	}
}
func (b *storyBuilder) authority(id string, primitive ResourceRef, node string) {
	var authority AuthorityEstablished
	if !b.source(id, "ExecutionAuthorityEstablished", &authority) {
		return
	}
	if !sameResource(authority.Workflow, b.workflow) || !sameResource(authority.Primitive, primitive) || !b.validResource(authority.StepAttempt, "StepAttempt", id) {
		b.issue("evidence mismatch", id, "execution authority identity differs")
		return
	}
	b.link(node, b.resource(authority.StepAttempt, "", id), "authorized_by", id)
}

func (b *storyBuilder) validation(artifact ArtifactEvidence, artifactNode string) {
	found := false
	for _, e := range b.events {
		if e.Type != "ValidationEvaluated" || e.Subject.Workflow != b.workflow.Name || e.Subject.Namespace != b.workflow.Namespace {
			continue
		}
		var decision DecisionEvaluated
		if json.Unmarshal(e.Data, &decision) != nil || decision.Primitive.UID != artifact.Producer.UID {
			continue
		}
		found = true
		if !b.decode(e, &decision) || !sameResource(decision.Primitive, artifact.Producer) || !b.validResource(decision.Primitive, "ValidationRun", e.ID) {
			b.issue("evidence mismatch", e.ID, "validation producer identity differs")
			continue
		}
		id := b.resource(decision.Primitive, decision.Decision.Outcome, e.ID)
		for i := range b.story.Nodes {
			if b.story.Nodes[i].ID == id {
				b.story.Nodes[i].AccessURL = e.References["validationURL"]
				b.story.Nodes[i].ProviderRef = e.References["providerRef"]
			}
		}
		b.link(artifactNode, id, "produced_by", e.ID)
		b.authority(decision.AuthorityEvent, decision.Primitive, id)
		var inputs InputsResolved
		if b.source(decision.InputEvent, "InputsResolved", &inputs) {
			if !sameResource(inputs.Consumer, decision.Primitive) {
				b.issue("evidence mismatch", decision.InputEvent, "validation input consumer differs")
				continue
			}
			for _, input := range inputs.Inputs {
				if !b.validResource(input.Artifact, "Artifact", decision.InputEvent) {
					continue
				}
				pin := v1alpha1.ArtifactReference{Name: strings.Split(input.Contract, "/")[0], Digest: input.Digest, ArtifactRef: &v1alpha1.UIDReference{Name: input.Artifact.Name, UID: types.UID(input.Artifact.UID)}}
				b.link(id, b.pin(pin, decision.InputEvent), "used_inputs_from", decision.InputEvent)
			}
		}
	}
	if !found {
		b.issue("missing evidence", b.story.EventID, "validation evaluation is unavailable for "+artifact.Producer.Name)
	}
}

func (b *storyBuilder) agentAuthorization(retry WorkflowRetryRecorded) {
	found := false
	for _, e := range b.events {
		if e.Type != "AgentExecutionAuthorized" || e.Subject.Workflow != b.workflow.Name || e.Subject.Namespace != b.workflow.Namespace {
			continue
		}
		var decision DecisionEvaluated
		if json.Unmarshal(e.Data, &decision) != nil || decision.Primitive.UID != retry.Execution.UID {
			continue
		}
		found = true
		if !b.decode(e, &decision) || !sameResource(decision.Primitive, retry.Execution) || decision.Decision.SchemaVersion != PayloadSchemaVersionV1 || decision.Decision.Outcome != "allowed" {
			b.issue("evidence mismatch", e.ID, "replacement execution authorization differs")
			continue
		}
		var authority AuthorityEstablished
		if !b.source(decision.AuthorityEvent, "ExecutionAuthorityEstablished", &authority) {
			continue
		}
		if !sameResource(authority.Workflow, b.workflow) || !sameResource(authority.Primitive, retry.Execution) || !sameResource(authority.StepAttempt, retry.Attempt) {
			b.issue("evidence mismatch", decision.AuthorityEvent, "replacement authority differs")
			continue
		}
		node := b.node(LineageNode{ID: "event:" + e.ID, Kind: "AgentAuthorization", Label: retry.Execution.Name, Outcome: "allowed", SourceEvents: []string{e.ID}})
		b.link(b.resource(retry.Execution, "", e.ID), node, "authorized_by", e.ID)
		b.link(node, b.resource(retry.Attempt, "", decision.AuthorityEvent), "authorized_by", decision.AuthorityEvent)
		b.inputs(decision.InputEvent, retry.Execution, retry.Inputs, b.resource(retry.Attempt, "", e.ID))
	}
	if !found {
		b.issue("missing evidence", b.story.EventID, "replacement execution authorization has not been recorded")
	}
}
