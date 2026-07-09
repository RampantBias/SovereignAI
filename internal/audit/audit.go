package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type Subject struct {
	Project   string `json:"project,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Workflow  string `json:"workflow,omitempty"`
	Step      string `json:"step,omitempty"`
	Attempt   int32  `json:"attempt,omitempty"`
}

type Event struct {
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	SchemaVersion string            `json:"schemaVersion"`
	OccurredAt    time.Time         `json:"occurredAt"`
	RecordedAt    time.Time         `json:"recordedAt,omitempty"`
	Actor         Actor             `json:"actor"`
	Requester     *Actor            `json:"requester,omitempty"`
	Subject       Subject           `json:"subject"`
	Action        string            `json:"action"`
	Target        string            `json:"target,omitempty"`
	Outcome       string            `json:"outcome"`
	Reason        string            `json:"reason,omitempty"`
	CorrelationID string            `json:"correlationId"`
	CausationID   string            `json:"causationId,omitempty"`
	DecisionID    string            `json:"decisionId,omitempty"`
	References    map[string]string `json:"references,omitempty"`
	Data          json.RawMessage   `json:"data,omitempty"`
}

type Recorder interface {
	// Record new event
	Append(context.Context, Event) error

	// List events related to a workflow
	ListWorkflow(context.Context, string) ([]Event, error)

	// Has event of type
	Has(string) bool
}

// DeterministicID creates a retry-safe identifier from stable domain keys.
func DeterministicID(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}
