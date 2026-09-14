package audit

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
)

// WorkflowView joins live navigation to existing evidence. It is a read-time
// snapshot, not a reconstruction of every state transition from the audit log.
type WorkflowView struct {
	SchemaVersion string             `json:"schemaVersion"`
	View          string             `json:"view"`
	Workflow      ResourceRef        `json:"workflow"`
	Phase         string             `json:"phase"`
	ExportedAt    time.Time          `json:"exportedAt"`
	Steps         []WorkflowStepView `json:"steps"`
	Attempts      []AttemptView      `json:"attempts"`
	Stories       []LineageStory     `json:"stories"`
	Issues        []string           `json:"issues"`
}

type WorkflowStepView struct {
	Name             string                        `json:"name"`
	Kind             v1alpha1.ExecutionKind        `json:"kind"`
	Order            int                           `json:"order"`
	Approval         *v1alpha1.ApprovalSpec        `json:"approval,omitempty"`
	RequiresApproval *v1alpha1.ApprovalRequirement `json:"requiresApproval,omitempty"`
}

type AttemptView struct {
	Resource    ResourceRef                `json:"resource"`
	Step        string                     `json:"step"`
	Kind        v1alpha1.ExecutionKind     `json:"kind"`
	Iteration   int32                      `json:"iteration"`
	Retry       int32                      `json:"retry"`
	CreatedAt   time.Time                  `json:"createdAt"`
	Status      v1alpha1.StepAttemptStatus `json:"status"`
	Execution   *ResourceRef               `json:"execution,omitempty"`
	Approval    *ApprovalRequestView       `json:"approval,omitempty"`
	Checkpoints []CheckpointView           `json:"checkpoints"`
	StoryIDs    []string                   `json:"storyIds"`
	Issues      []string                   `json:"issues"`
}

type ApprovalRequestView struct {
	Resource ResourceRef                  `json:"resource"`
	Phase    v1alpha1.ResourcePhase       `json:"phase"`
	Policy   v1alpha1.ApprovalSpec        `json:"policy"`
	Inputs   []v1alpha1.ArtifactReference `json:"inputs"`
}

type CheckpointView struct {
	EventID         string            `json:"eventId"`
	Type            string            `json:"type"`
	OccurredAt      time.Time         `json:"occurredAt"`
	Outcome         string            `json:"outcome"`
	Reason          string            `json:"reason,omitempty"`
	Binding         string            `json:"binding"`
	AccessURL       string            `json:"accessURL,omitempty"`
	Evidence        any               `json:"evidence,omitempty"`
	RelatedAttempts map[string]string `json:"relatedAttempts,omitempty"`
}

func BuildWorkflowView(wf *v1alpha1.SovereignWorkflow, attempts []v1alpha1.StepAttempt, approvals []v1alpha1.ApprovalRequest, events []Event) (WorkflowView, error) {
	ref := func(kind, name, uid string) ResourceRef {
		return ResourceRef{SchemaVersion: PayloadSchemaVersionV1, APIVersion: v1alpha1.GroupVersion.String(), Kind: kind, Namespace: wf.Namespace, Name: name, UID: uid}
	}
	v := WorkflowView{SchemaVersion: PayloadSchemaVersionV1, View: "workflow", Workflow: ref("SovereignWorkflow", wf.Name, string(wf.UID)), Phase: wf.Status.Phase, ExportedAt: time.Now().UTC(), Steps: []WorkflowStepView{}, Attempts: []AttemptView{}, Issues: []string{}}
	for _, step := range wf.Spec.Steps {
		v.Steps = append(v.Steps, WorkflowStepView{step.Name, step.Kind, step.Order, step.Approval, step.RequiresApproval})
	}
	sort.SliceStable(v.Steps, func(i, j int) bool { return v.Steps[i].Order < v.Steps[j].Order })
	for _, a := range attempts {
		if a.Namespace != wf.Namespace || a.Spec.WorkflowRef.Name != wf.Name || a.Spec.WorkflowRef.UID != wf.UID || a.UID == "" {
			continue
		}
		// Export only useful status, not completion payloads or arbitrary conditions.
		status := v1alpha1.StepAttemptStatus{Phase: a.Status.Phase, ExecutionRef: a.Status.ExecutionRef, FailureReason: a.Status.FailureReason, FailureMessage: a.Status.FailureMessage, Retryable: a.Status.Retryable, StartedAt: a.Status.StartedAt, CompletedAt: a.Status.CompletedAt}
		item := AttemptView{Resource: ref("StepAttempt", a.Name, string(a.UID)), Step: a.Spec.StepName, Kind: a.Spec.Kind, Iteration: a.Spec.WorkflowAttempt, Retry: a.Spec.RetryNumber, CreatedAt: a.CreationTimestamp.Time, Status: status, Checkpoints: []CheckpointView{}, StoryIDs: []string{}, Issues: []string{}}
		for _, request := range approvals {
			if request.Namespace == wf.Namespace && request.Spec.WorkflowRef.Name == wf.Name && request.Spec.WorkflowRef.UID == wf.UID && request.Spec.AttemptRef.Name == a.Name && request.Spec.AttemptRef.UID == a.UID && request.UID != "" {
				item.Approval = &ApprovalRequestView{ref("ApprovalRequest", request.Name, string(request.UID)), request.Status.Phase, request.Spec.Approval, request.Spec.Inputs}
			}
		}
		v.Attempts = append(v.Attempts, item)
	}
	sort.Slice(v.Attempts, func(i, j int) bool {
		a, b := v.Attempts[i], v.Attempts[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		// Kubernetes timestamps can tie; workflow counters/order are a display
		// tie-break, never an inferred causal relationship.
		if a.Iteration != b.Iteration {
			return a.Iteration < b.Iteration
		}
		order := func(name string) int {
			for i, s := range v.Steps {
				if s.Name == name {
					return i
				}
			}
			return len(v.Steps)
		}
		if order(a.Step) != order(b.Step) {
			return order(a.Step) < order(b.Step)
		}
		if a.Retry != b.Retry {
			return a.Retry < b.Retry
		}
		return a.Resource.UID < b.Resource.UID
	})
	var scoped []Event
	for _, e := range events {
		if e.Subject.Workflow != wf.Name || e.Subject.Namespace != wf.Namespace {
			continue
		}
		var identity struct {
			Workflow ResourceRef `json:"workflow"`
		}
		_ = json.Unmarshal(e.Data, &identity)
		if identity.Workflow.UID != "" && identity.Workflow.UID != string(wf.UID) {
			continue
		}
		scoped = append(scoped, e)
	}
	// Runtime utility events omit namespace. Include only their known payloads,
	// joined through exact execution authority and decision references in this scope.
	primitives := map[string]ResourceRef{}
	decisions := map[string]ResourceRef{}
	for _, e := range scoped {
		var authority AuthorityEstablished
		if e.Type == "ExecutionAuthorityEstablished" && json.Unmarshal(e.Data, &authority) == nil && sameResource(authority.Workflow, v.Workflow) {
			primitives[authority.Primitive.UID] = authority.Primitive
		}
		var decision DecisionEvaluated
		if e.Type == "UtilityExecutionAuthorized" && json.Unmarshal(e.Data, &decision) == nil {
			decisions[e.ID] = decision.Primitive
		}
	}
	for _, e := range events {
		if e.Subject.Workflow != wf.Name || e.Subject.Namespace != "" || e.Type != "CandidatePreparationEvaluated" {
			continue
		}
		var p DecisionEvaluated
		if json.Unmarshal(e.Data, &p) == nil && p.SchemaVersion == PayloadSchemaVersionV1 && sameResource(p.Primitive, primitives[p.Primitive.UID]) {
			scoped = append(scoped, e)
			decisions[e.ID] = p.Primitive
		}
	}
	for _, e := range events {
		if e.Subject.Workflow != wf.Name || e.Subject.Namespace != "" || e.Type != "UtilityOperationCompleted" {
			continue
		}
		var p ConsequenceRecorded
		if json.Unmarshal(e.Data, &p) != nil || p.SchemaVersion != PayloadSchemaVersionV1 {
			continue
		}
		primitive := decisions[p.DecisionEvent]
		if sameResource(primitive, primitives[primitive.UID]) && e.References["utilityOperation"] == primitive.Name {
			scoped = append(scoped, e)
		}
	}
	projection, err := BuildProjection(wf.Name, "", scoped)
	if err != nil {
		return v, err
	}
	v.Stories = projection.Stories
	sort.Slice(scoped, func(i, j int) bool {
		if scoped[i].OccurredAt.Equal(scoped[j].OccurredAt) {
			return scoped[i].ID < scoped[j].ID
		}
		return scoped[i].OccurredAt.Before(scoped[j].OccurredAt)
	})
	for i := range v.Attempts {
		a := &v.Attempts[i]
		for _, e := range scoped {
			var authority AuthorityEstablished
			if e.Type != "ExecutionAuthorityEstablished" || json.Unmarshal(e.Data, &authority) != nil {
				continue
			}
			if authority.SchemaVersion != PayloadSchemaVersionV1 || !sameResource(authority.Workflow, v.Workflow) || !sameResource(authority.StepAttempt, a.Resource) {
				continue
			}
			if r := a.Status.ExecutionRef; r != nil && r.Kind == authority.Primitive.Kind && r.Name == authority.Primitive.Name && authority.Primitive.UID != "" {
				if a.Execution != nil && !sameResource(*a.Execution, authority.Primitive) {
					a.Issues = append(a.Issues, "Conflicting execution identities in authority evidence.")
					a.Execution = nil
					break
				}
				execution := authority.Primitive
				a.Execution = &execution
			}
		}
		for _, story := range v.Stories {
			for _, node := range story.Nodes {
				if (node.Resource != nil && a.matches(*node.Resource)) || (node.Approval != nil && string(node.Approval.StepAttempt.UID) == a.Resource.UID) {
					a.StoryIDs = append(a.StoryIDs, story.EventID)
					break
				}
			}
		}
		seen := map[string]bool{}
		for _, e := range scoped {
			checkpoint, belongs := a.checkpoint(e)
			if belongs && !seen[e.ID] {
				a.Checkpoints = append(a.Checkpoints, checkpoint)
				seen[e.ID] = true
			}
		}
		if a.Status.Phase == v1alpha1.PhaseFailed || a.Status.Phase == v1alpha1.PhaseInterrupted {
			found := false
			for _, c := range a.Checkpoints {
				if c.Type == "StepAttempt"+string(a.Status.Phase) {
					found = true
				}
			}
			if !found {
				a.Issues = append(a.Issues, "Failure is present in live StepAttempt status, but its terminal audit event is unavailable.")
			}
		}
	}
	return v, nil
}

func (a AttemptView) matches(ref ResourceRef) bool {
	if ref.Namespace != a.Resource.Namespace || ref.UID == "" {
		return false
	}
	if sameResource(ref, a.Resource) {
		return true
	}
	if a.Execution != nil && sameResource(ref, *a.Execution) {
		return true
	}
	return a.Approval != nil && sameResource(ref, a.Approval.Resource)
}

// Decode only established evidence types: unknown event data and private context
// bytes never pass through into the HTML. Typed identities take precedence over names.
func (a AttemptView) checkpoint(e Event) (CheckpointView, bool) {
	c := CheckpointView{EventID: e.ID, Type: e.Type, OccurredAt: e.OccurredAt, Outcome: e.Outcome, Reason: e.Reason, Binding: "resource UID"}
	var payload any
	var belongs func() bool
	switch e.Type {
	case "InputsResolved":
		p := &InputsResolved{}
		payload = p
		belongs = func() bool { return a.matches(p.Consumer) }
	case "ExecutionAuthorityEstablished":
		p := &AuthorityEstablished{}
		payload = p
		belongs = func() bool { return a.matches(p.StepAttempt) && a.matches(p.Primitive) }
	case "AgentExecutionAuthorized", "UtilityExecutionAuthorized", "UtilityOperationAdmitted", "ValidationEvaluated", "CandidatePreparationEvaluated":
		p := &DecisionEvaluated{}
		payload = p
		belongs = func() bool { return a.matches(p.Primitive) }
	case "RecoveryDecisionSelected":
		p := &WorkflowRecoveryDecision{}
		payload = p
		belongs = func() bool {
			return (p.Recovery.TriggerAttempt.Name == a.Resource.Name && string(p.Recovery.TriggerAttempt.UID) == a.Resource.UID) || (p.Recovery.PreviousAttempt != nil && p.Recovery.PreviousAttempt.Name == a.Resource.Name && string(p.Recovery.PreviousAttempt.UID) == a.Resource.UID)
		}
		if e.References["failedAttempt"] == a.Resource.Name || e.References["retryAttempt"] == a.Resource.Name {
			c.Binding = "resource name (legacy recovery reference)"
			belongs = func() bool { return true }
		}
	case "ArtifactAccepted":
		p := &ConsequenceRecorded{}
		payload = p
		belongs = func() bool {
			for _, result := range p.Consequences {
				if result.Artifact != nil && a.matches(result.Artifact.Producer) {
					return true
				}
			}
			return false
		}
	case "ApprovalDecisionSubmitted", "ApprovalDecisionAdmitted":
		p := &ApprovalDecisionEvidence{}
		payload = p
		belongs = func() bool {
			return p.StepAttempt.Name == a.Resource.Name && string(p.StepAttempt.UID) == a.Resource.UID
		}
	case "ContextSnapshotSaved":
		p := &ContextSnapshotEvidence{}
		payload = p
		belongs = func() bool { return a.matches(p.Attempt) && a.matches(p.Execution) }
	case "StepAttemptRetried":
		p := &WorkflowRetryRecorded{}
		if json.Unmarshal(e.Data, p) == nil && p.DecisionEvent != "" {
			payload = p
			belongs = func() bool {
				return a.matches(p.Attempt) || (p.TriggeredBy.Name == a.Resource.Name && string(p.TriggeredBy.UID) == a.Resource.UID) || (p.RetryOf != nil && p.RetryOf.Name == a.Resource.Name && string(p.RetryOf.UID) == a.Resource.UID)
			}
		}
	}
	if payload != nil {
		var version struct {
			SchemaVersion string `json:"schemaVersion"`
		}
		if e.SchemaVersion != PayloadSchemaVersionV1 || json.Unmarshal(e.Data, &version) != nil || version.SchemaVersion != PayloadSchemaVersionV1 || json.Unmarshal(e.Data, payload) != nil || !belongs() {
			return c, false
		}
		c.Evidence = payload
		if e.Type == "RecoveryDecisionSelected" && strings.HasPrefix(c.Binding, "resource name") {
			c.RelatedAttempts = map[string]string{"failed_attempt": e.References["failedAttempt"], "planned_retry": e.References["retryAttempt"]}
		}
		if e.Type == "ValidationEvaluated" {
			c.AccessURL = e.References["validationURL"]
		}
		return c, true
	}
	// Older lifecycle and runtime events identify their subject by name. Expose
	// that limitation instead of pretending these carry the typed UID guarantee.
	if e.Subject.Step != a.Step || (e.Subject.Attempt != a.Retry && e.References["failedAttempt"] != a.Resource.Name && e.References["retryAttempt"] != a.Resource.Name) {
		return c, false
	}
	match := e.Target == a.Resource.Name
	if r := a.Status.ExecutionRef; r != nil {
		key := map[string]string{"UtilityOperation": "utilityOperation", "AgentRun": "agentRun", "ApprovalRequest": "approvalRequest", "ValidationRun": "validationRun"}[r.Kind]
		match = match || e.Target == r.Name || (key != "" && e.References[key] == r.Name)
	}
	match = match || e.References["failedAttempt"] == a.Resource.Name || e.References["retryAttempt"] == a.Resource.Name
	if !match {
		return c, false
	}
	c.Binding = "resource name (legacy audit envelope)"
	if e.Type == "UtilityOperationCompleted" && e.Subject.Namespace == "" {
		c.Binding = "decision event and execution reference"
	}
	if e.Type == "StepAttemptRetried" {
		c.RelatedAttempts = map[string]string{"retry_of": e.References["failedAttempt"], "planned_retry": e.References["retryAttempt"]}
	}
	if strings.HasSuffix(e.Type, "Completed") || strings.HasSuffix(e.Type, "Succeeded") || strings.HasSuffix(e.Type, "Failed") {
		p := &ConsequenceRecorded{}
		if json.Unmarshal(e.Data, p) == nil && p.SchemaVersion == PayloadSchemaVersionV1 {
			c.Evidence = p
		}
	}
	return c, true
}
