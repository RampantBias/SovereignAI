package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
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

type MemoryRecorder struct {
	mu     sync.RWMutex
	events map[string]Event
}

func NewMemoryRecorder() *MemoryRecorder {
	return &MemoryRecorder{events: make(map[string]Event)}
}

func (m *MemoryRecorder) Append(_ context.Context, event Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.events == nil {
		m.events = make(map[string]Event)
	}
	if _, exists := m.events[event.ID]; exists {
		return nil
	}
	if event.RecordedAt.IsZero() {
		event.RecordedAt = time.Now().UTC()
	}
	m.events[event.ID] = event
	return nil
}

func (m *MemoryRecorder) ListWorkflow(_ context.Context, workflow string) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Get events associated with the workflow
	events := make([]Event, 0)
	for _, event := range m.events {
		if event.Subject.Workflow == workflow {
			events = append(events, event)
		}
	}

	// Sort events by the time they occurred
	sort.Slice(events, func(i, j int) bool {
		if events[i].OccurredAt.Equal(events[j].OccurredAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].OccurredAt.Before(events[j].OccurredAt)
	})
	return events, nil
}

func (m *MemoryRecorder) Has(eventType string) bool {
	for _, event := range m.events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func (m *MemoryRecorder) AllEvents() []Event {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Get events associated with the workflow
	events := make([]Event, 0)
	for _, event := range m.events {
		events = append(events, event)
	}

	// Sort events by the time they occurred
	sort.Slice(events, func(i, j int) bool {
		if events[i].OccurredAt.Equal(events[j].OccurredAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].OccurredAt.Before(events[j].OccurredAt)
	})
	return events
}
