package exechistory

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"darek/db"

	"github.com/jackc/pgx/v5"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Recorder is an OpenTelemetry SpanProcessor that writes execution-root
// spans and their darek/* descendants to Postgres.
//
// Children are buffered in memory keyed by TraceID until the execution-root
// span ends; then execution + children are flushed in a single transaction.
// On Shutdown, any orphaned pending steps are dropped — by design, we never
// surface a partial execution.
type Recorder struct {
	pool *db.Pool
	log  *slog.Logger

	mu      sync.Mutex
	pending map[trace.TraceID][]stepRow
}

func NewRecorder(pool *db.Pool, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.Default()
	}
	return &Recorder{
		pool:    pool,
		log:     log,
		pending: make(map[trace.TraceID][]stepRow),
	}
}

func (r *Recorder) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (r *Recorder) OnEnd(s sdktrace.ReadOnlySpan) {
	if !strings.HasPrefix(s.InstrumentationScope().Name, scopePrefix) {
		return
	}
	if _, ok := executionKind(s); ok {
		r.flushExecution(s)
		return
	}
	r.bufferStep(s)
}

func (r *Recorder) bufferStep(s sdktrace.ReadOnlySpan) {
	row, err := spanToStepRow(s)
	if err != nil {
		r.log.Warn("exechistory: map step", "err", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		// Shutdown has run; drop silently.
		return
	}
	tid := s.SpanContext().TraceID()
	r.pending[tid] = append(r.pending[tid], row)
}

func (r *Recorder) flushExecution(s sdktrace.ReadOnlySpan) {
	r.mu.Lock()
	tid := s.SpanContext().TraceID()
	steps := r.pending[tid]
	delete(r.pending, tid)
	r.mu.Unlock()

	totals := sumLLMTotals(steps)
	execRow, err := spanToExecutionRow(s, totals)
	if err != nil {
		r.log.Warn("exechistory: map execution", "err", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := r.writeAll(ctx, execRow, steps); err != nil {
		r.log.Warn("exechistory: write", "err", err, "trace_id", tid.String())
	}
}

func (r *Recorder) writeAll(ctx context.Context, exec executionRow, steps []stepRow) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
INSERT INTO executions (id, trace_id, span_id, kind, name, started_at, ended_at, duration_ms, status, error, attributes,
                        total_tokens_in, total_tokens_out, total_tokens_cached, total_cost_usd)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, NULLIF($10,''), $11, $12, $13, $14, $15)`,
		exec.ID, exec.TraceID, exec.SpanID, exec.Kind, exec.Name,
		exec.StartedAt, exec.EndedAt, exec.DurationMS,
		exec.Status, exec.Error, exec.Attributes,
		exec.TotalTokensIn, exec.TotalTokensOut, exec.TotalTokensCached, exec.TotalCostUSD); err != nil {
		return err
	}

	if len(steps) > 0 {
		batch := &pgx.Batch{}
		for _, sr := range steps {
			batch.Queue(`
INSERT INTO execution_steps (id, execution_id, parent_span_id, span_id, name, started_at, ended_at, duration_ms, status, error, attributes, events)
VALUES ($1,$2, NULLIF($3,''), $4,$5,$6,$7,$8,$9, NULLIF($10,''), $11, $12)`,
				sr.ID, exec.ID, sr.ParentSpanID, sr.SpanID, sr.Name,
				sr.StartedAt, sr.EndedAt, sr.DurationMS,
				sr.Status, sr.Error, sr.Attributes, sr.Events)
		}
		br := tx.SendBatch(ctx, batch)
		for range steps {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Shutdown discards all pending step rows whose execution-root span never ended.
// Returns nil; errors are logged.
func (r *Recorder) Shutdown(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) > 0 {
		r.log.Info("exechistory: dropping pending steps", "traces", len(r.pending))
	}
	r.pending = nil
	return nil
}

// ForceFlush is a no-op for this processor: we have nothing buffered that
// should be eagerly written without an execution-root span.
func (r *Recorder) ForceFlush(context.Context) error { return nil }
