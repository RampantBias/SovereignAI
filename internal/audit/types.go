package audit

const PayloadSchemaVersionV1 = "v1"

// This is the culmination of the various events,
// I can associate individual audit references into
// this combined record through regular joins
type DecisionEvidence struct {
	SchemaVersion   string `json:"schemaVersion"`
	DecisionEventID string `json:"decisionEventId"`

	Authority    AuthorityEstablished  `json:"authority"`
	Inputs       []ArtifactEvidence    `json:"inputs"`
	Decision     DecisionEvaluated     `json:"decision"`
	Invariants   []InvariantResult     `json:"invariants"`
	Consequences []ConsequenceEvidence `json:"consequences"`

	SourceEventIDs []SourceEventRef `json:"sourceEventIds"`
	Verification   Verification     `json:"verification"`
}

type SourceEventRef struct {
	SchemaVersion string `json:"schemaVersion"`
	Role          string `json:"role"`
	EventID       string `json:"eventId"`
}

type Verification struct {
	SchemaVersion string   `json:"schemaVersion"`
	Status        string   `json:"status"`
	Missing       []string `json:"missing"`
}

// Links event to unique Kubernetes object
type ResourceRef struct {
	SchemaVersion string `json:"schemaVersion"`
	APIVersion    string `json:"apiVersion"`
	Kind          string `json:"kind"`
	Namespace     string `json:"namespace,omitempty"`
	Name          string `json:"name"`
	UID           string `json:"uid"`
}

// Links a decision to the exact content and contract
// to the primitive
type ArtifactEvidence struct {
	SchemaVersion  string       `json:"schemaVersion"`
	Artifact       ResourceRef  `json:"artifact"`
	Contract       string       `json:"contract"`
	Digest         string       `json:"digest"`
	Producer       ResourceRef  `json:"producer"`
	ProducerGrant  *ResourceRef `json:"producerGrant,omitempty"`
	SourceRevision string       `json:"sourceRevision,omitempty"`
	Classification string       `json:"classification,omitempty"`
}

// Why the decision passed or failed
type InvariantResult struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Outcome       string `json:"outcome"` // passed, failed, not-evaluated
	Expected      string `json:"expected,omitempty"`
	Observed      string `json:"observed,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// which policy, human decision, or other criteria produced
// the result and with what input
type DecisionRef struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Kind          string `json:"kind"` // policy, human, scheduler, controller
	Revision      string `json:"revision,omitempty"`
	SourceDigest  string `json:"sourceDigest,omitempty"`
	InputDigest   string `json:"inputDigest,omitempty"`
	Outcome       string `json:"outcome"` // allowed, denied, selected
}

// ties execution write authority to trace
type WorkspaceAuthority struct {
	SchemaVersion  string      `json:"schemaVersion"`
	Lease          ResourceRef `json:"lease"`
	HolderIdentity string      `json:"holderIdentity"`
	WriterEpoch    int32       `json:"writerEpoch"`
	Capabilities   []string    `json:"capabilities"`
	// could consider adding credential info if human sessions make a comeback
}

// Proves authority delegation ownership via events
// like ExecutionAuthorityEstablished
type AuthorityEstablished struct {
	SchemaVersion  string              `json:"schemaVersion"`
	Workflow       ResourceRef         `json:"workflow"`
	StepAttempt    ResourceRef         `json:"stepAttempt"`
	Primitive      ResourceRef         `json:"primitive"`
	WorkspaceWrite *WorkspaceAuthority `json:"workspaceWrite,omitempty"`
	Invariants     []InvariantResult   `json:"invariants"`
}

// Proves what inputs were consumed
type InputsResolved struct {
	SchemaVersion string             `json:"schemaVersion"`
	Consumer      ResourceRef        `json:"consumer"`
	Inputs        []ArtifactEvidence `json:"inputs"`
}

// Links authority and inputs to primitive producer
type DecisionEvaluated struct {
	SchemaVersion  string            `json:"schemaVersion"`
	Primitive      ResourceRef       `json:"primitive"`
	Decision       DecisionRef       `json:"decision"`
	AuthorityEvent string            `json:"authorityEvent,omitempty"`
	InputEvent     string            `json:"inputEvent,omitempty"`
	EvidenceEvents []string          `json:"evidenceEvents,omitempty"`
	Invariants     []InvariantResult `json:"invariants"`
}

// Links allowed action to outcome
type ConsequenceEvidence struct {
	SchemaVersion string            `json:"schemaVersion"`
	Kind          string            `json:"kind"`
	Resource      *ResourceRef      `json:"resource,omitempty"`
	Artifact      *ArtifactEvidence `json:"artifactEvidence,omitempty"`
	Digest        string            `json:"digest,omitempty"`
	ExternalID    string            `json:"externalId,omitempty"`
	Target        string            `json:"target,omitempty"`
	Before        string            `json:"before,omitempty"`
	After         string            `json:"after,omitempty"`
}

type ConsequenceRecorded struct {
	SchemaVersion string                `json:"schemaVersion"`
	DecisionEvent string                `json:"decisionEvent"`
	Consequences  []ConsequenceEvidence `json:"consequences"`
}
