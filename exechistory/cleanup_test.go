//go:build integration

package exechistory

import (
	"context"
	"testing"
	"time"

	"darek/db"
	pgtest "darek/internal/testutil/pg"

	"github.com/google/uuid"
)

func TestRunCleanupOnce_DeletesOldRowsAndCascadesSteps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, raw := pgtest.Start(t)
	setupSchema(t, ctx, raw)
	pool := db.Wrap(raw)

	// One row from 10 days ago, one from now.
	oldID, newID := uuid.New(), uuid.New()
	now := time.Now().UTC()
	old := now.Add(-10 * 24 * time.Hour)
	for _, row := range []struct {
		id uuid.UUID
		t  time.Time
	}{{oldID, old}, {newID, now}} {
		insertExecution(t, ctx, pool, Execution{
			ID: row.id, TraceID: "t", SpanID: uuid.NewString()[:16],
			Kind: "k", Name: "n", StartedAt: row.t, EndedAt: row.t,
			DurationMS: 1, Status: "ok",
		})
		_, _ = pool.Exec(ctx, `
INSERT INTO execution_steps (id, execution_id, span_id, name, started_at, ended_at, duration_ms, status, attributes, events)
VALUES ($1,$2,$3,'s',$4,$4,0,'ok','{}'::jsonb,'[]'::jsonb)`,
			uuid.New(), row.id, uuid.NewString()[:16], row.t)
	}

	deleted, err := runCleanupOnce(ctx, pool, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted: got %d want 1", deleted)
	}

	var execCount, stepCount int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM executions`).Scan(&execCount)
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM execution_steps`).Scan(&stepCount)
	if execCount != 1 {
		t.Errorf("execCount: got %d want 1", execCount)
	}
	if stepCount != 1 {
		t.Errorf("stepCount: got %d want 1 (CASCADE failed)", stepCount)
	}
}
