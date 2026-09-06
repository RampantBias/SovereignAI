//go:build integration

package audit

import (
	"bytes"
	"context"
	"errors"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Run explicitly against a disposable PostgreSQL instance. Each run isolates its schema.
func TestPostgresContextTransaction(t *testing.T) {
	dsn := os.Getenv("SOVEREIGN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("SOVEREIGN_TEST_POSTGRES_DSN is required for integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schemaName := "context_test_" + time.Now().UTC().Format("20060102150405000000")
	identifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE")
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schemaName
	recorder, err := OpenPostgres(ctx, config.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	makeSnapshot := func(run string) ContextSnapshot {
		content := []byte("{ \"private\": \"initial context\" }\n")
		return ContextSnapshot{Bytes: content, Evidence: ContextSnapshotEvidence{SchemaVersion: "v1", Workflow: projectionRef("SovereignWorkflow", "wf"), Attempt: projectionRef("StepAttempt", run), Execution: projectionRef("AgentRun", run), SnapshotID: DeterministicID("initial-context/v1", run+"-uid"), Digest: artifactcontract.DigestBytes(content), Format: ContextFormat, ByteCount: len(content), SavedAt: time.Now().UTC()}}
	}
	snapshot := makeSnapshot("author")
	saved, err := recorder.SaveContext(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			copy := snapshot
			copy.Evidence.SavedAt = time.Now().UTC()
			receipt, err := recorder.SaveContext(ctx, copy)
			if err != nil || !reflect.DeepEqual(receipt, saved) {
				t.Errorf("idempotent receipt: %v", err)
			}
		}()
	}
	group.Wait()
	result, err := recorder.GetContext(ctx, saved.SnapshotID)
	if err != nil || !bytes.Equal(result.Bytes, snapshot.Bytes) {
		t.Fatalf("exact bytes did not survive: %v", err)
	}
	events, err := recorder.ListWorkflow(ctx, "wf")
	if err != nil || len(events) != 1 || bytes.Contains(events[0].Data, []byte("initial context")) {
		t.Fatalf("metadata event isolation: %v", err)
	}
	changed := snapshot
	changed.Bytes = []byte(`{"private":"different"}`)
	changed.Evidence.Digest = artifactcontract.DigestBytes(changed.Bytes)
	changed.Evidence.ByteCount = len(changed.Bytes)
	if _, err := recorder.SaveContext(ctx, changed); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("changed initial context accepted: %v", err)
	}
	if _, err := recorder.pool.Exec(ctx, `ALTER TABLE audit_events ADD CONSTRAINT reject_context CHECK (event_type <> 'ContextSnapshotSaved') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	failed := makeSnapshot("retry")
	if _, err := recorder.SaveContext(ctx, failed); err == nil {
		t.Fatal("expected receipt insertion failure")
	}
	if _, err := recorder.GetContext(ctx, failed.Evidence.SnapshotID); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatal("snapshot insert did not roll back with audit event")
	}
	if _, err := recorder.pool.Exec(ctx, `ALTER TABLE audit_events DROP CONSTRAINT reject_context`); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.SaveContext(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.pool.Exec(ctx, `UPDATE private_context_snapshots SET content=$1 WHERE id=$2`, []byte(`{}`), saved.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.GetContext(ctx, saved.SnapshotID); err == nil {
		t.Fatal("retrieval did not detect content corruption")
	}
}
