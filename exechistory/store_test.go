//go:build integration

package exechistory

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"darek/db"
	pgtest "darek/internal/testutil/pg"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setupSchema(t *testing.T, ctx context.Context, raw *pgxpool.Pool) {
	t.Helper()
	if err := db.Migrate(ctx, raw); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func insertExecution(t *testing.T, ctx context.Context, p *db.Pool, e Execution) {
	t.Helper()
	attrs, _ := json.Marshal(e.Attributes)
	_, err := p.Exec(ctx, `
INSERT INTO executions (id, trace_id, span_id, kind, name, started_at, ended_at, duration_ms, status, error, attributes)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		e.ID, e.TraceID, e.SpanID, e.Kind, e.Name, e.StartedAt, e.EndedAt, e.DurationMS, e.Status, e.Error, attrs)
	if err != nil {
		t.Fatalf("insert execution: %v", err)
	}
}

func TestStore_List_Empty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, raw := pgtest.Start(t)
	setupSchema(t, ctx, raw)
	pool := db.Wrap(raw)

	s := NewStore(pool)
	got, err := s.List(ctx, ListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 rows, got %d", len(got))
	}
}

func TestStore_List_FilterByKind_OrdersNewestFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, raw := pgtest.Start(t)
	setupSchema(t, ctx, raw)
	pool := db.Wrap(raw)
	store := NewStore(pool)

	t0 := time.Now().Add(-2 * time.Hour).UTC()
	for i, kind := range []string{"freshrss-sync", "chat-turn", "freshrss-sync"} {
		insertExecution(t, ctx, pool, Execution{
			ID:         uuid.New(),
			TraceID:    "trace",
			SpanID:     uuid.NewString()[:16],
			Kind:       kind,
			Name:       "n",
			StartedAt:  t0.Add(time.Duration(i) * time.Minute),
			EndedAt:    t0.Add(time.Duration(i) * time.Minute).Add(time.Second),
			DurationMS: 1000,
			Status:     "ok",
		})
	}

	got, err := store.List(ctx, ListFilter{Kind: "freshrss-sync", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 freshrss-sync rows, got %d", len(got))
	}
	if got[0].StartedAt.Before(got[1].StartedAt) {
		t.Errorf("rows not ordered newest-first")
	}
}

func TestStore_Kinds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, raw := pgtest.Start(t)
	setupSchema(t, ctx, raw)
	pool := db.Wrap(raw)
	store := NewStore(pool)

	for _, k := range []string{"a", "b", "a", "c"} {
		insertExecution(t, ctx, pool, Execution{
			ID: uuid.New(), TraceID: "t", SpanID: uuid.NewString()[:16],
			Kind: k, Name: "n", StartedAt: time.Now(), EndedAt: time.Now(),
			DurationMS: 1, Status: "ok",
		})
	}

	kinds, err := store.Kinds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 3 {
		t.Errorf("want 3 distinct kinds, got %d (%v)", len(kinds), kinds)
	}
}

func TestStore_Get_ReturnsStepsInOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, raw := pgtest.Start(t)
	setupSchema(t, ctx, raw)
	pool := db.Wrap(raw)
	store := NewStore(pool)

	id := uuid.New()
	insertExecution(t, ctx, pool, Execution{
		ID: id, TraceID: "t", SpanID: "0123456789abcdef",
		Kind: "k", Name: "n",
		StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC().Add(time.Second),
		DurationMS: 1000, Status: "ok",
	})

	// Two steps, inserted in reverse chronological order to verify ORDER BY.
	for i, off := range []time.Duration{500 * time.Millisecond, 100 * time.Millisecond} {
		_, err := pool.Exec(ctx, `
INSERT INTO execution_steps (id, execution_id, parent_span_id, span_id, name, started_at, ended_at, duration_ms, status, attributes, events)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'{}'::jsonb,'[]'::jsonb)`,
			uuid.New(), id, "0123456789abcdef", uuid.NewString()[:16],
			[]string{"second", "first"}[i],
			time.Now().UTC().Add(off), time.Now().UTC().Add(off).Add(10*time.Millisecond),
			10, "ok",
		)
		if err != nil {
			t.Fatalf("insert step %d: %v", i, err)
		}
	}

	exec, steps, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if exec.ID != id {
		t.Errorf("exec id mismatch")
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[0].Name != "first" || steps[1].Name != "second" {
		t.Errorf("step order wrong: %q, %q", steps[0].Name, steps[1].Name)
	}
}
