package audit

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Build in-memory recorder for testing/debug
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

func (m *MemoryRecorder) ListWorkflow(_ context.Context, workflowName string) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Get events associated with the workflow
	events := make([]Event, 0)
	for _, event := range m.events {
		if event.Subject.Workflow == workflowName {
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

// Probably a leaky abstraction?
func (m *MemoryRecorder) Has(eventType string) bool {
	for _, event := range m.events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

// This is a bit of a leaky abstraction
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
