//go:build integration

package exechistory

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"darek/db"
	pgtest "darek/internal/testutil/pg"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func newRecorderTracer(t *testing.T, pool *db.Pool) (*sdktrace.TracerProvider, *Recorder) {
	t.Helper()
	rec := NewRecorder(pool, slog.Default())
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp, rec
}

func TestRecorder_HappyPath_OneExecutionThreeSteps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, raw := pgtest.Start(t)
	setupSchema(t, ctx, raw)
	pool := db.Wrap(raw)
	tp, _ := newRecorderTracer(t, pool)

	tr := tp.Tracer("darek/freshrssimport")
	rootCtx, root := tr.Start(ctx, "freshrssimport.sync")
	root.SetAttributes(attribute.String(KindAttribute, "freshrss-sync"))
	for i, name := range []string{"fetch", "parse", "store"} {
		_, child := tr.Start(rootCtx, name)
		child.SetAttributes(attribute.Int("step.idx", i))
		child.End()
	}
	root.End()

	var execCount, stepCount int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM executions`).Scan(&execCount)
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM execution_steps`).Scan(&stepCount)
	if execCount != 1 {
		t.Errorf("executions count: got %d want 1", execCount)
	}
	if stepCount != 3 {
		t.Errorf("execution_steps count: got %d want 3", stepCount)
	}

	var kind, status string
	_ = pool.QueryRow(ctx, `SELECT kind, status FROM executions LIMIT 1`).Scan(&kind, &status)
	if kind != "freshrss-sync" || status != "ok" {
		t.Errorf("kind/status: got (%q,%q)", kind, status)
	}
}

func TestRecorder_IgnoresNonDarekScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, raw := pgtest.Start(t)
	setupSchema(t, ctx, raw)
	pool := db.Wrap(raw)
	tp, _ := newRecorderTracer(t, pool)

	tr := tp.Tracer("otelpgx") // not darek/*
	_, span := tr.Start(ctx, "SELECT 1")
	span.SetAttributes(attribute.String(KindAttribute, "noise"))
	span.End()

	var execCount int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM executions`).Scan(&execCount)
	if execCount != 0 {
		t.Errorf("expected 0 executions, got %d", execCount)
	}
}

func TestRecorder_ErrorStatusPropagates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, raw := pgtest.Start(t)
	setupSchema(t, ctx, raw)
	pool := db.Wrap(raw)
	tp, _ := newRecorderTracer(t, pool)

	tr := tp.Tracer("darek/x")
	_, root := tr.Start(ctx, "thing")
	root.SetAttributes(attribute.String(KindAttribute, "x"))
	root.SetStatus(codes.Error, "boom")
	root.RecordError(errors.New("boom"))
	root.End()

	var status, errMsg string
	_ = pool.QueryRow(ctx, `SELECT status, COALESCE(error,'') FROM executions LIMIT 1`).Scan(&status, &errMsg)
	if status != "error" || errMsg != "boom" {
		t.Errorf("status/error: got (%q,%q) want (error, boom)", status, errMsg)
	}
}

func TestRecorder_ShutdownDropsOrphanedSteps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, raw := pgtest.Start(t)
	setupSchema(t, ctx, raw)
	pool := db.Wrap(raw)
	tp, rec := newRecorderTracer(t, pool)

	tr := tp.Tracer("darek/x")
	rootCtx, root := tr.Start(ctx, "root")
	root.SetAttributes(attribute.String(KindAttribute, "x"))
	_, child := tr.Start(rootCtx, "child")
	child.End() // child ends, root does NOT

	if err := rec.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	var execCount, stepCount int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM executions`).Scan(&execCount)
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM execution_steps`).Scan(&stepCount)
	if execCount != 0 || stepCount != 0 {
		t.Errorf("orphan flush leaked rows: exec=%d step=%d", execCount, stepCount)
	}
	_ = root // root is intentionally left unended; the deferred tp.Shutdown will discard it.
}
