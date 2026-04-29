package db

import (
	"context"
	"fmt"
	"time"

	"darek/obs"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Pool wraps *pgxpool.Pool and records uniform dep=postgres metrics per call.
// Sub-queries inside an explicit Begin() transaction are NOT recorded as
// individual metrics (their spans still come from otelpgx); the tx_begin
// observation captures the transaction itself.
type Pool struct {
	inner *pgxpool.Pool
	m     *obs.Metrics
}

func Open(ctx context.Context, dsn string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.ConnConfig.Tracer = otelpgx.NewTracer()
	inner, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := inner.Ping(ctx); err != nil {
		inner.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	pool := &Pool{inner: inner}
	if m, err := obs.MetricsInstance(); err == nil {
		pool.m = m
	}
	return pool, nil
}

func (p *Pool) Close() { p.inner.Close() }

// Inner returns the wrapped *pgxpool.Pool. Use sparingly — for migrations and
// other code that needs the raw pool. Day-to-day store code should use the
// wrapper methods so metrics get recorded.
func (p *Pool) Inner() *pgxpool.Pool { return p.inner }

// Stat returns pool statistics. Used by obs.RegisterPoolGauges.
func (p *Pool) Stat() *pgxpool.Stat { return p.inner.Stat() }

func (p *Pool) record(ctx context.Context, op string, start time.Time, err error) {
	if p.m == nil {
		return
	}
	dur := time.Since(start).Seconds()
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	attrs := metric.WithAttributes(
		attribute.String("dep", "postgres"),
		attribute.String("op", op),
		attribute.String("outcome", outcome),
	)
	p.m.DepRequests.Add(ctx, 1, attrs)
	p.m.DepLatency.Record(ctx, dur, attrs)
}

// Query records dep=postgres,op=query.
func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	start := time.Now()
	rows, err := p.inner.Query(ctx, sql, args...)
	p.record(ctx, "query", start, err)
	return rows, err
}

// QueryRow records dep=postgres,op=query. pgx returns a non-nil Row even on
// error; the error surfaces from Scan. Latency is recorded at call time;
// outcome here is "ok" — Scan-time errors are not reflected.
func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	start := time.Now()
	row := p.inner.QueryRow(ctx, sql, args...)
	p.record(ctx, "query", start, nil)
	return row
}

// Exec records dep=postgres,op=exec.
func (p *Pool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	start := time.Now()
	tag, err := p.inner.Exec(ctx, sql, args...)
	p.record(ctx, "exec", start, err)
	return tag, err
}

// Begin records dep=postgres,op=tx_begin and returns the raw pgx.Tx.
// Sub-queries inside the transaction are not metric-recorded.
func (p *Pool) Begin(ctx context.Context) (pgx.Tx, error) {
	start := time.Now()
	tx, err := p.inner.Begin(ctx)
	p.record(ctx, "tx_begin", start, err)
	return tx, err
}
