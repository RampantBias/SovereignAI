package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type FileRecorder struct {
	path string
}

func NewFileRecorder(path string) *FileRecorder {
	return &FileRecorder{path: path}
}

func (r *FileRecorder) Append(_ context.Context, event Event) error {
	if r == nil || r.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o750); err != nil {
		return fmt.Errorf("create audit event directory: %w", err)
	}
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("open audit event file: %w", err)
	}
	defer file.Close()
	if event.RecordedAt.IsZero() {
		event.RecordedAt = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

func (r *FileRecorder) ListWorkflow(_ context.Context, workflowName string) ([]Event, error) {
	events, err := ReadEventFile(r.path)
	if err != nil {
		return nil, err
	}
	filtered := events[:0]
	for _, event := range events {
		if event.Subject.Workflow == workflowName || event.CorrelationID == workflowName {
			filtered = append(filtered, event)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].OccurredAt.Equal(filtered[j].OccurredAt) {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].OccurredAt.Before(filtered[j].OccurredAt)
	})
	return filtered, nil
}

func (r *FileRecorder) Has(eventType string) bool {
	events, err := ReadEventFile(r.path)
	if err != nil {
		return false
	}
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func ReadEventFile(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open audit event file: %w", err)
	}
	defer file.Close()

	var events []Event
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode audit event line %d: %w", line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read audit event file: %w", err)
	}
	return events, nil
}

func IngestEventFile(ctx context.Context, recorder Recorder, path string) (int, error) {
	if recorder == nil || path == "" {
		return 0, nil
	}
	events, err := ReadEventFile(path)
	if err != nil {
		return 0, err
	}
	for _, event := range events {
		if err := recorder.Append(ctx, event); err != nil {
			return 0, fmt.Errorf("append runtime audit event %q: %w", event.ID, err)
		}
	}
	return len(events), nil
}
