package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/inference"
)

const ContextFormat = "sovereign.ai/initial-context/v1"
const MaxContextBytes = 2 * 1024 * 1024

var ErrSnapshotNotFound = errors.New("context snapshot not found")
var ErrSnapshotConflict = errors.New("initial context already saved with different content")

// InitialContext contains only model context, never upload credentials or environment.
type InitialContext struct {
	SchemaVersion string                       `json:"schemaVersion"`
	Request       inference.ChatRequest        `json:"request"`
	Artifacts     []ContextArtifact            `json:"artifacts"`
	RetryFeedback *agentcontract.RetryFeedback `json:"retryFeedback,omitempty"`
}
type ContextArtifact struct {
	Metadata agentcontract.ArtifactInput `json:"metadata"`
	Content  string                      `json:"content"`
}

// ContextSnapshotEvidence is safe for the ordinary audit timeline. Raw bytes live separately.
type ContextSnapshotEvidence struct {
	SchemaVersion string                       `json:"schemaVersion"`
	Workflow      ResourceRef                  `json:"workflow"`
	Attempt       ResourceRef                  `json:"attempt"`
	Execution     ResourceRef                  `json:"execution"`
	SnapshotID    string                       `json:"snapshotId"`
	Digest        string                       `json:"digest"`
	Format        string                       `json:"format"`
	ByteCount     int                          `json:"byteCount"`
	Inputs        []v1alpha1.ArtifactReference `json:"inputs"`
	SavedAt       time.Time                    `json:"savedAt"`
}
type ContextSnapshot struct {
	Evidence ContextSnapshotEvidence
	Bytes    []byte
}

// SaveContext atomically persists private bytes and the reference-only audit event.
// Repeated equal writes return the original receipt; changed bytes are rejected.
type SnapshotStore interface {
	SaveContext(context.Context, ContextSnapshot) (ContextSnapshotEvidence, error)
	GetContext(context.Context, string) (ContextSnapshot, error)
}

func ContextEventID(id string) string { return DeterministicID("ContextSnapshotSaved", id) }
func ContextEvent(e ContextSnapshotEvidence) (Event, error) {
	event, err := NewEvent(EventOptions{Source: "api-server", Type: "ContextSnapshotSaved", OccurredAt: e.SavedAt,
		Actor: Actor{Kind: "AgentRun", ID: e.Execution.UID}, Subject: Subject{Namespace: e.Execution.Namespace, Workflow: e.Workflow.Name},
		Action: "save-context", Target: e.Execution.Name, Outcome: "saved", Data: e})
	event.ID = ContextEventID(e.SnapshotID)
	return event, err
}
func ValidateSnapshot(snapshot ContextSnapshot) error {
	e := snapshot.Evidence
	if len(snapshot.Bytes) == 0 || len(snapshot.Bytes) > MaxContextBytes || !json.Valid(snapshot.Bytes) {
		return fmt.Errorf("invalid snapshot size or JSON")
	}
	if e.SchemaVersion != PayloadSchemaVersionV1 || e.Format != ContextFormat || e.SnapshotID != DeterministicID("initial-context/v1", e.Execution.UID) ||
		e.Digest != artifactcontract.DigestBytes(snapshot.Bytes) || e.ByteCount != len(snapshot.Bytes) || e.Workflow.UID == "" || e.Attempt.UID == "" || e.Execution.UID == "" || e.SavedAt.IsZero() {
		return fmt.Errorf("snapshot evidence does not match its content or identity")
	}
	return nil
}
