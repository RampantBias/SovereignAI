package audit

// This is the culmination of the various events,
// I can associate individual audit references into
// this combined record through regular joins
type DecisionEvidenceV1 struct {
	DecisionEventId string        `json:"decisionEventId"`
	Workflow        ResourceRefV1 `json:"workflow"`
	StepAttempt     ResourceRefV1 `json:"stepAttempt"`
	Primitive       ResourceRefV1 `json:"primitive"`

	Authority    AuthorityEstablishedV1  `json:"authority"`
	Inputs       []ArtifactEvidenceV1    `json:"inputs"`
	Decision     DecisionEvaluatedV1     `json:"decision"`
	Invariants   []InvariantResultV1     `json:"invariants"`
	Consequences []ConsequenceEvidenceV1 `json:"consequences"`

	SourceEventIDs []string `json:"sourceEventIds"`
	Verification   string   `json:"verification"`
}

// Links event to unique Kubernetes object
type ResourceRefV1 struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
}

// Links a decision to the exact content and contract
// to the primitive
type ArtifactEvidenceV1 struct {
	Artifact       ResourceRefV1  `json:"artifact"`
	Contract       string         `json:"contract"`
	Digest         string         `json:"digest"`
	Producer       ResourceRefV1  `json:"producer"`
	ProducerGrant  *ResourceRefV1 `json:"producerGrant,omitempty"`
	SourceRevision string         `json:"sourceRevision,omitempty"`
	Classification string         `json:"classification,omitempty"`
}

// Why the decision passed or failed
type InvariantResultV1 struct {
	ID       string `json:"id"`
	Outcome  string `json:"outcome"` // passed, failed, not-evaluated
	Expected string `json:"expected,omitempty"`
	Observed string `json:"observed,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// which policy, human decision, or other criteria produced
// the result and with what input
type DecisionRefV1 struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // policy, human, scheduler, controller
	Revision     string `json:"revision,omitempty"`
	SourceDigest string `json:"sourceDigest,omitempty"`
	InputDigest  string `json:"inputDigest,omitempty"`
	Outcome      string `json:"outcome"` // allowed, denied, selected
}

// ties execution write authority to trace
type WorkspaceAuthorityV1 struct {
	Lease          ResourceRefV1 `json:"lease"`
	HolderIdentity string        `json:"holderIdentity"`
	WriterEpoch    int32         `json:"writerEpoch"`
}

// Proves authority delegation ownership via events
// like ExecutionAuthorityEstablished
type AuthorityEstablishedV1 struct {
	Workflow       ResourceRefV1         `json:"workflow"`
	StepAttempt    ResourceRefV1         `json:"stepAttempt"`
	Primitive      ResourceRefV1         `json:"primitive"`
	WorkspaceWrite *WorkspaceAuthorityV1 `json:"workspaceWrite,omitempty"`
	Invariants     []InvariantResultV1   `json:"invariants"`
}

// Proves what inputs were consumed
type InputsResolvedV1 struct {
	Consumer ResourceRefV1        `json:"consumer"`
	Inputs   []ArtifactEvidenceV1 `json:"inputs"`
}

// Links authority and inputs to primitive producer
type DecisionEvaluatedV1 struct {
	Primitive      ResourceRefV1       `json:"primitive"`
	Decision       DecisionRefV1       `json:"decision"`
	AuthorityEvent string              `json:"authorityEvent"`
	InputEvent     string              `json:"inputEvent,omitempty"`
	EvidenceEvents []string            `json:"evidenceEvents,omitempty"`
	Invariants     []InvariantResultV1 `json:"invariants"`
}

// Links allowed action to outcome
type ConsequenceEvidenceV1 struct {
	Kind       string         `json:"kind"`
	Resource   *ResourceRefV1 `json:"resource,omitempty"`
	Digest     string         `json:"digest,omitempty"`
	ExternalID string         `json:"externalId,omitempty"`
	Target     string         `json:"target,omitempty"`
	Before     string         `json:"before,omitempty"`
	After      string         `json:"after,omitempty"`
}

type ConsequenceRecordedV1 struct {
	DecisionEvent string                  `json:"decisionEvent"`
	Consequences  []ConsequenceEvidenceV1 `json:"consequences"`
}
