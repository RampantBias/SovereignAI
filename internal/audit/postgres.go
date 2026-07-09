package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
CREATE TABLE IF NOT EXISTS audit_events (
  id text PRIMARY KEY,
  event_type text NOT NULL,
  schema_version text NOT NULL,
  occurred_at timestamptz NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  workflow_id text NOT NULL DEFAULT '',
  correlation_id text NOT NULL,
  causation_id text NOT NULL DEFAULT '',
  payload jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_events_workflow_time
  ON audit_events (workflow_id, occurred_at, id);`

type PostgresRecorder struct {
	pool *pgxpool.Pool
}

func OpenPostgres(ctx context.Context, dsn string) (*PostgresRecorder, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open audit database: %w", err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("initialize audit schema: %w", err)
	}
	return &PostgresRecorder{pool: pool}, nil
}

func (p *PostgresRecorder) Close() { p.pool.Close() }

func (p *PostgresRecorder) Append(ctx context.Context, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal audit event: %w", err)
	}
	_, err = p.pool.Exec(ctx, `
INSERT INTO audit_events (id,event_type,schema_version,occurred_at,workflow_id,correlation_id,causation_id,payload)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (id) DO NOTHING`, event.ID, event.Type, event.SchemaVersion, event.OccurredAt,
		event.Subject.Workflow, event.CorrelationID, event.CausationID, payload)
	if err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

func (p *PostgresRecorder) ListWorkflow(ctx context.Context, workflow string) ([]Event, error) {
	rows, err := p.pool.Query(ctx, `SELECT payload FROM audit_events WHERE workflow_id=$1 ORDER BY occurred_at,id`, workflow)
	if err != nil {
		return nil, fmt.Errorf("query audit timeline: %w", err)
	}
	defer rows.Close()
	events := make([]Event, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan audit timeline: %w", err)
		}
		var event Event
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, fmt.Errorf("decode audit event: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (m *PostgresRecorder) Has(eventType string) bool {
	// Search for first occurrence of event type
	return false
}
