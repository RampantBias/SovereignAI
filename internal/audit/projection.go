package audit

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
	return Projection{}, nil
}
