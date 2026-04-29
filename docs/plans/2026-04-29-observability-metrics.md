# Observability metrics & Grafana dashboards — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make darek fully observable from one Grafana tab — cost, activity, and health — by adding uniform external-dependency metrics, business-activity counters, runtime + DB pool metrics, and a coherent set of seven dashboards.

**Architecture:** A single `obs.Dep` helper records `darek.dep.{requests,latency}` at every external call site (uniform `dep`+`op`+`outcome` labels). A thin `db.Pool` wrapper records `dep=postgres` automatically for stores. Business counters (`darek.mail.*`, `darek.memory.*`, `darek.links.events`) are bumped at well-defined hook points. Existing LLM-specific metrics (`tokens.*`, `cost_usd`, `llm.latency`) stay; nothing is removed. Seven JSON dashboards under `otel/grafana/dashboards/`.

**Tech Stack:** Go 1.22+, OpenTelemetry Go SDK (already wired), `go.opentelemetry.io/contrib/instrumentation/runtime`, pgx/v5 (already used), Prometheus + Grafana 11 (already provisioned).

**Spec:** [`docs/specs/2026-04-29-observability-metrics-design.md`](../specs/2026-04-29-observability-metrics-design.md)

**Out of scope for this plan:** alerts, log-based metrics, retention tuning, OpenAI embeddings (no call site exists today), distributed-tracing improvements beyond what already exists.

---

## File Map

| Path | Responsibility |
|---|---|
| `obs/metrics.go` | Add new instruments (dep, agent, mail, memory, links, db pool gauges). |
| `obs/metrics_test.go` | Tests for new instruments. |
| `obs/dep.go` | `Dep(ctx, dep, op, fn)` helper — wraps a call with span + dep metrics. |
| `obs/dep_test.go` | Tests for `Dep`. |
| `obs/runtime.go` | `StartRuntime()` — registers Go runtime instrumentation. |
| `obs/db_pool.go` | `RegisterPoolGauges(pool)` — async gauges on `pgxpool.Pool.Stat()`. |
| `obs/cardinality_test.go` | Test asserting no free-form labels are recorded. |
| `obs/otel.go` | Call `StartRuntime()` from `Init`. |
| `db/pool.go` | New `*db.Pool` wrapper around `*pgxpool.Pool` (Query/Exec/QueryRow/Begin record metrics). |
| `db/pool_test.go` | Tests for the wrapper. |
| `memory/store.go` | Take `*db.Pool`; bump `memory.notes_{saved,recalled}` counters. |
| `links/store.go` | Take `*db.Pool`; bump `links.events` counters with op label. |
| `cmd/darek/chat.go` | Update store constructors to pass `*db.Pool`. |
| `cmd/darek/migrate.go` | Update to use new pool type if needed (only if it touches stores). |
| `llm/client.go` | Wrap `Chat` with `obs.Dep("openai_chat","chat",...)`. |
| `agent/agent.go` | Bump `agent.max_iters_hit` counter. |
| `tools/calendar/google/google.go` | Wrap API call with `obs.Dep("google_calendar","list_events",...)`. |
| `tools/calendar/ical/ical.go` | Wrap HTTP fetch with `obs.Dep("ical","fetch",...)`. |
| `tools/todoist/client.go` | Wrap `doJSON` with `obs.Dep("todoist", op, ...)`. |
| `tools/freshrss/client.go` | Wrap `authedDo` with `obs.Dep("freshrss", op, ...)`. |
| `tools/mail/imap/imap.go` | Wrap each IMAP method with `obs.Dep("imap", op, ...)`; bump business counters. |
| `tools/mail/imap/append.go` | Wrap `Append` with `obs.Dep("imap","append",...)`. |
| `tools/mail/smtp/smtp.go` | Wrap `Send` with `obs.Dep("smtp","send",...)`; bump `mail.sent`. |
| `otel/grafana/dashboards/agent_turns.json` | Refresh: add max_iters_hit, error rate, outcome breakdown. |
| `otel/grafana/dashboards/tokens_and_cost.json` | Refresh: tokens by kind stack, cache hit %. |
| `otel/grafana/dashboards/tools.json` | Renamed from `tool_latency.json`; refresh with error rate, p50/p95/p99. |
| `otel/grafana/dashboards/external_deps.json` | NEW: per-dep RPS, p50/p95, error rate, top-slow. |
| `otel/grafana/dashboards/mail.json` | NEW: sync rate, fetches, sends, IMAP error rate. |
| `otel/grafana/dashboards/links_memory.json` | NEW: links events, search/similar p95, notes saved/recalled. |
| `otel/grafana/dashboards/runtime.json` | NEW: goroutines, GC, heap, pgx pool gauges, db p95. |
| `otel/grafana/dashboards/overview.json` | NEW: single pane of glass — KPIs, activity, cost, health. |

---

## Conventions

- All new instruments live under the `darek.*` namespace, registered in `obs/metrics.go`.
- All histograms use seconds (`metric.WithUnit("s")`).
- Metric labels use **only fixed enums** — never user input, URLs, IDs, or error strings. Enforced by `obs/cardinality_test.go`.
- `obs.Dep` is the single helper for any network or process-boundary call; it records both span and metrics so call sites stop calling `tracer.Start` directly for these cases.
- The `db.Pool` wrapper records `dep=postgres` per top-level call. Sub-queries inside an explicit `Begin` transaction are not metric-recorded (their spans still come from `otelpgx`); for now we accept this — the app rarely uses long transactions.
- Frequent commits: each task ends with a `git commit`. Commits go to whatever branch the worker is on.

---

## Task 1 — Add new metric instruments

**Files:**
- Modify: `obs/metrics.go`
- Modify: `obs/metrics_test.go`

- [ ] **Step 1: Write failing tests for the new instruments**

Add to `obs/metrics_test.go` (after the existing tests):

```go
func TestMetricsInstance_HasNewInstruments(t *testing.T) {
	m, err := obs.MetricsInstance()
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if m.DepRequests == nil {
		t.Error("DepRequests not initialized")
	}
	if m.DepLatency == nil {
		t.Error("DepLatency not initialized")
	}
	if m.AgentMaxItersHit == nil {
		t.Error("AgentMaxItersHit not initialized")
	}
	if m.MailEnvelopesSynced == nil {
		t.Error("MailEnvelopesSynced not initialized")
	}
	if m.MailBodiesFetched == nil {
		t.Error("MailBodiesFetched not initialized")
	}
	if m.MailAttachmentsFetched == nil {
		t.Error("MailAttachmentsFetched not initialized")
	}
	if m.MailSent == nil {
		t.Error("MailSent not initialized")
	}
	if m.MemoryNotesSaved == nil {
		t.Error("MemoryNotesSaved not initialized")
	}
	if m.MemoryNotesRecalled == nil {
		t.Error("MemoryNotesRecalled not initialized")
	}
	if m.LinksEvents == nil {
		t.Error("LinksEvents not initialized")
	}
}
```

If `metrics_test.go` doesn't already import `darek/obs` under that alias, match its existing import style (the existing file may use the package directly with `package obs`).

- [ ] **Step 2: Run the test to verify it fails**

```
go test ./obs -run TestMetricsInstance_HasNewInstruments
```

Expected: FAIL — fields don't exist on the struct.

- [ ] **Step 3: Add the new instruments to `obs/metrics.go`**

Replace the `Metrics` struct and `MetricsInstance()` with:

```go
package obs

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

type Metrics struct {
	// LLM-specific (kept)
	TokensInput  metric.Int64Counter
	TokensOutput metric.Int64Counter
	TokensCached metric.Int64Counter
	LLMLatency   metric.Float64Histogram
	LLMCostUSD   metric.Float64Counter

	// In-process work
	ToolCalls        metric.Int64Counter
	ToolLatency      metric.Float64Histogram
	TurnDuration     metric.Float64Histogram
	TurnIters        metric.Int64Histogram
	AgentMaxItersHit metric.Int64Counter

	// External dependencies (uniform)
	DepRequests metric.Int64Counter
	DepLatency  metric.Float64Histogram

	// Mail business activity
	MailEnvelopesSynced    metric.Int64Counter
	MailBodiesFetched      metric.Int64Counter
	MailAttachmentsFetched metric.Int64Counter
	MailSent               metric.Int64Counter

	// Memory
	MemoryNotesSaved    metric.Int64Counter
	MemoryNotesRecalled metric.Int64Counter

	// Links
	LinksEvents metric.Int64Counter
}

var (
	metricsOnce sync.Once
	metricsInst *Metrics
	metricsErr  error
)

func MetricsInstance() (*Metrics, error) {
	metricsOnce.Do(func() {
		m := otel.Meter("darek")
		var err error
		i64 := func(c metric.Int64Counter, e error) metric.Int64Counter {
			if e != nil && err == nil {
				err = e
			}
			return c
		}
		f64hist := func(c metric.Float64Histogram, e error) metric.Float64Histogram {
			if e != nil && err == nil {
				err = e
			}
			return c
		}
		i64hist := func(c metric.Int64Histogram, e error) metric.Int64Histogram {
			if e != nil && err == nil {
				err = e
			}
			return c
		}
		f64ctr := func(c metric.Float64Counter, e error) metric.Float64Counter {
			if e != nil && err == nil {
				err = e
			}
			return c
		}
		metricsInst = &Metrics{
			TokensInput:            i64(m.Int64Counter("darek.tokens.input")),
			TokensOutput:           i64(m.Int64Counter("darek.tokens.output")),
			TokensCached:           i64(m.Int64Counter("darek.tokens.cached")),
			LLMLatency:             f64hist(m.Float64Histogram("darek.llm.latency", metric.WithUnit("s"))),
			LLMCostUSD:             f64ctr(m.Float64Counter("darek.llm.cost_usd", metric.WithUnit("USD"))),
			ToolCalls:              i64(m.Int64Counter("darek.tool.calls")),
			ToolLatency:            f64hist(m.Float64Histogram("darek.tool.latency", metric.WithUnit("s"))),
			TurnDuration:           f64hist(m.Float64Histogram("darek.turn.duration", metric.WithUnit("s"))),
			TurnIters:              i64hist(m.Int64Histogram("darek.turn.iterations")),
			AgentMaxItersHit:       i64(m.Int64Counter("darek.agent.max_iters_hit")),
			DepRequests:            i64(m.Int64Counter("darek.dep.requests")),
			DepLatency:             f64hist(m.Float64Histogram("darek.dep.latency", metric.WithUnit("s"))),
			MailEnvelopesSynced:    i64(m.Int64Counter("darek.mail.envelopes_synced")),
			MailBodiesFetched:      i64(m.Int64Counter("darek.mail.bodies_fetched")),
			MailAttachmentsFetched: i64(m.Int64Counter("darek.mail.attachments_fetched")),
			MailSent:               i64(m.Int64Counter("darek.mail.sent")),
			MemoryNotesSaved:       i64(m.Int64Counter("darek.memory.notes_saved")),
			MemoryNotesRecalled:    i64(m.Int64Counter("darek.memory.notes_recalled")),
			LinksEvents:            i64(m.Int64Counter("darek.links.events")),
		}
		metricsErr = err
	})
	return metricsInst, metricsErr
}
```

- [ ] **Step 4: Run tests**

```
go test ./obs/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add obs/metrics.go obs/metrics_test.go
git commit -m "feat(obs): new dep, agent, mail, memory, links metric instruments"
```

---

## Task 2 — `obs.Dep` helper

**Files:**
- Create: `obs/dep.go`
- Create: `obs/dep_test.go`

The helper wraps any external call so labels stay uniform. Signature:

```go
func Dep(ctx context.Context, dep, op string, fn func(context.Context) error) error
```

- [ ] **Step 1: Write the failing test**

Create `obs/dep_test.go`:

```go
package obs_test

import (
	"context"
	"errors"
	"testing"

	"darek/obs"
)

func TestDep_CallsFnExactlyOnce(t *testing.T) {
	calls := 0
	err := obs.Dep(context.Background(), "openai_chat", "chat", func(ctx context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
}

func TestDep_PropagatesError(t *testing.T) {
	want := errors.New("boom")
	got := obs.Dep(context.Background(), "openai_chat", "chat", func(ctx context.Context) error {
		return want
	})
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDep_RejectsEmptyDepOrOp(t *testing.T) {
	if err := obs.Dep(context.Background(), "", "chat", func(ctx context.Context) error { return nil }); err == nil {
		t.Error("expected error on empty dep")
	}
	if err := obs.Dep(context.Background(), "openai_chat", "", func(ctx context.Context) error { return nil }); err == nil {
		t.Error("expected error on empty op")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```
go test ./obs -run TestDep
```

Expected: FAIL — `obs.Dep` undefined.

- [ ] **Step 3: Implement `obs.Dep`**

Create `obs/dep.go`:

```go
package obs

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
)

var depTracer = otel.Tracer("darek/dep")

// Dep wraps an external call: starts a span, runs fn, records dep.requests
// and dep.latency with uniform labels. Use this instead of tracer.Start for
// any call that crosses a network or process boundary.
//
// dep is a fixed enum: openai_chat, google_calendar, todoist, freshrss, ical,
// imap, smtp, postgres. op is a per-dep enum (e.g. imap: sync_folder).
// Never put user input, URLs, IDs, or error strings in either label.
func Dep(ctx context.Context, dep, op string, fn func(context.Context) error) error {
	if dep == "" {
		return fmt.Errorf("obs.Dep: dep is required")
	}
	if op == "" {
		return fmt.Errorf("obs.Dep: op is required")
	}
	m, err := MetricsInstance()
	if err != nil {
		return fmt.Errorf("obs.Dep metrics: %w", err)
	}
	ctx, span := depTracer.Start(ctx, dep+"."+op,
		trace.WithAttributes(
			attribute.String("dep", dep),
			attribute.String("op", op),
		),
	)
	defer span.End()

	start := time.Now()
	err = fn(ctx)
	dur := time.Since(start).Seconds()

	outcome := "ok"
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	attrs := metric.WithAttributes(
		attribute.String("dep", dep),
		attribute.String("op", op),
		attribute.String("outcome", outcome),
	)
	m.DepRequests.Add(ctx, 1, attrs)
	m.DepLatency.Record(ctx, dur, attrs)
	return err
}
```

You will also need this import (already used elsewhere in `obs`):

```go
import "go.opentelemetry.io/otel/trace"
```

- [ ] **Step 4: Run the tests**

```
go test ./obs -run TestDep -v
```

Expected: PASS for all three cases.

- [ ] **Step 5: Commit**

```
git add obs/dep.go obs/dep_test.go
git commit -m "feat(obs): Dep helper for uniform dep metrics + spans"
```

---

## Task 3 — Runtime instrumentation

**Files:**
- Create: `obs/runtime.go`
- Modify: `obs/otel.go`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the runtime contrib dep**

```
go get go.opentelemetry.io/contrib/instrumentation/runtime
```

- [ ] **Step 2: Create `obs/runtime.go`**

```go
package obs

import (
	"fmt"

	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
)

// StartRuntime registers Go runtime instrumentation (goroutines, GC, heap)
// against the global meter provider. Safe to call after obs.Init has set
// the meter provider; idempotent once started.
func StartRuntime() error {
	if err := otelruntime.Start(); err != nil {
		return fmt.Errorf("runtime instrumentation: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Wire it into `obs.Init`**

In `obs/otel.go`, after the `otel.SetMeterProvider(mp)` line (around line 75), add:

```go
	if err := StartRuntime(); err != nil {
		return nil, nil, err
	}
```

The full `Init` ending block becomes:

```go
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	global.SetLoggerProvider(lp)

	if err := StartRuntime(); err != nil {
		return nil, nil, err
	}
```

- [ ] **Step 4: Verify it compiles and existing tests still pass**

```
go build ./...
go test ./obs/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add obs/runtime.go obs/otel.go go.mod go.sum
git commit -m "feat(obs): Go runtime instrumentation (goroutines, GC, heap)"
```

---

## Task 4 — Cardinality discipline test

**Files:**
- Create: `obs/cardinality_test.go`

This is a static test that asserts only allowed labels are passed to recording sites. We can't introspect attributes recorded historically; instead we provide a checked enum of allowed values per metric and assert at code-review time. Concretely: add a small `allowed.go` and a test that records a fake recording and inspects it via the manual reader pattern.

The simplest version is a smoke test that records sample observations through `Dep` for each dep+op pair and uses a manual reader to assert no surprise labels appear. This catches typos and accidental label additions in code review.

- [ ] **Step 1: Add the test**

Create `obs/cardinality_test.go`:

```go
package obs_test

import (
	"context"
	"testing"

	"darek/obs"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// allowedDeps is the closed set of dep names. Any new dep must be added here
// AND have an entry in allowedOps.
var allowedDeps = map[string]struct{}{
	"openai_chat":     {},
	"google_calendar": {},
	"todoist":         {},
	"freshrss":        {},
	"ical":            {},
	"imap":            {},
	"smtp":            {},
	"postgres":        {},
}

var allowedOps = map[string]map[string]struct{}{
	"openai_chat":     {"chat": {}},
	"google_calendar": {"list_events": {}},
	"todoist":         {"list_tasks": {}, "create_task": {}, "complete_task": {}, "update_task": {}},
	"freshrss":        {"login": {}, "list": {}, "get": {}, "mark": {}, "token": {}},
	"ical":            {"fetch": {}},
	"imap":            {"sync_folder": {}, "fetch_body": {}, "fetch_attachment": {}, "append": {}},
	"smtp":            {"send": {}},
	"postgres":        {"query": {}, "exec": {}, "tx_begin": {}},
}

var allowedAttrKeys = map[string]struct{}{"dep": {}, "op": {}, "outcome": {}}

func TestDep_OnlyAllowedLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	obs.ResetMetricsForTest()

	for dep, ops := range allowedOps {
		for op := range ops {
			_ = obs.Dep(context.Background(), dep, op, func(ctx context.Context) error { return nil })
		}
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "darek.dep.requests" && m.Name != "darek.dep.latency" {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					checkAttrs(t, dp.Attributes.ToSlice(), m.Name)
				}
			case metricdata.Histogram[float64]:
				for _, dp := range d.DataPoints {
					checkAttrs(t, dp.Attributes.ToSlice(), m.Name)
				}
			}
		}
	}
}

func checkAttrs(t *testing.T, attrs []attribute.KeyValue, metricName string) {
	t.Helper()
	for _, kv := range attrs {
		key := string(kv.Key)
		if _, ok := allowedAttrKeys[key]; !ok {
			t.Errorf("%s: unexpected label %q (only dep/op/outcome allowed)", metricName, key)
			continue
		}
		v := kv.Value.AsString()
		switch key {
		case "dep":
			if _, ok := allowedDeps[v]; !ok {
				t.Errorf("%s: unknown dep %q", metricName, v)
			}
		case "outcome":
			if v != "ok" && v != "error" {
				t.Errorf("%s: outcome must be ok|error, got %q", metricName, v)
			}
		}
	}
}
```

- [ ] **Step 2: Add `ResetMetricsForTest` to `obs/metrics.go`**

Tests need to reset the singleton so `MetricsInstance()` picks up a fresh meter provider. Add to `obs/metrics.go` (at the end of the file):

```go
// ResetMetricsForTest is used by tests to force MetricsInstance to rebuild
// against the current global meter provider. NOT for production use.
func ResetMetricsForTest() {
	metricsOnce = sync.Once{}
	metricsInst = nil
	metricsErr = nil
}
```

- [ ] **Step 3: Run the test**

```
go test ./obs -run TestDep_OnlyAllowedLabels -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add obs/cardinality_test.go obs/metrics.go
git commit -m "test(obs): cardinality test pinning allowed dep/op label values"
```

---

## Task 5 — `db.Pool` wrapper

**Files:**
- Modify: `db/pool.go`
- Create: `db/pool_test.go`

Wraps `*pgxpool.Pool`, recording `darek.dep.{requests,latency}` with `dep=postgres` for every top-level Query/Exec/QueryRow/Begin. Returned types preserve pgx's API.

- [ ] **Step 1: Write a failing test**

Create `db/pool_test.go`:

```go
//go:build integration

package db_test

import (
	"context"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testpg"
)

func TestPool_QueryRecordsMetrics(t *testing.T) {
	dsn := testpg.Start(t)
	pool, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&got); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
	// We can't easily assert on the recorded metric here without a manual reader;
	// the cardinality test in obs covers the label shape. This test asserts the
	// wrapper preserves the pgx semantics (non-nil row, scan works).
}
```

The integration test infrastructure (`darek/internal/testpg`) already exists per the codebase conventions; if it's named differently in this repo, swap the import and helper name to match (search for existing integration tests with `//go:build integration` to see the pattern).

- [ ] **Step 2: Run it to verify the wrapper doesn't compile yet**

```
go test -tags=integration ./db
```

Expected: FAIL — `db.Pool.QueryRow` undefined (Open returns `*pgxpool.Pool` today).

- [ ] **Step 3: Replace `db/pool.go`**

```go
package db

import (
	"context"
	"fmt"
	"time"

	"darek/obs"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Pool wraps *pgxpool.Pool to record uniform dep=postgres metrics per call.
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
	m, err := obs.MetricsInstance()
	if err != nil {
		inner.Close()
		return nil, fmt.Errorf("metrics: %w", err)
	}
	return &Pool{inner: inner, m: m}, nil
}

func (p *Pool) Close() { p.inner.Close() }

// Inner returns the wrapped *pgxpool.Pool. Use sparingly — for migrations and
// other code that needs the raw pool. Day-to-day store code should use the
// wrapper methods so metrics get recorded.
func (p *Pool) Inner() *pgxpool.Pool { return p.inner }

func (p *Pool) record(ctx context.Context, op string, start time.Time, err error) {
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
// error; the error surfaces from Scan. We record latency at call time; the
// outcome label here is "ok" — Scan-time errors are not reflected. This is a
// conscious tradeoff: tracking Row.Scan would require a wrapper Row type and
// adds complexity for marginal value.
func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	start := time.Now()
	row := p.inner.QueryRow(ctx, sql, args...)
	p.record(ctx, "query", start, nil)
	return row
}

// Exec records dep=postgres,op=exec.
func (p *Pool) Exec(ctx context.Context, sql string, args ...any) (pgconnCommandTag, error) {
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

// Stat returns pool statistics. Used by obs.RegisterPoolGauges.
func (p *Pool) Stat() *pgxpool.Stat { return p.inner.Stat() }

// pgconnCommandTag is the return type of pgxpool.Pool.Exec.
// We re-export it so callers don't need to import pgconn directly.
type pgconnCommandTag = pgconnCommandTagAlias
```

**Replace the alias** with the real type. `pgxpool.Pool.Exec` returns `pgconn.CommandTag`. Add the import `"github.com/jackc/pgx/v5/pgconn"` and use `pgconn.CommandTag` directly:

```go
func (p *Pool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
```

Drop the `pgconnCommandTag` alias and the placeholder line at the bottom of the file.

- [ ] **Step 4: Run the integration test**

```
make test-integration
```

(or `go test -tags=integration ./db`). Expected: PASS.

- [ ] **Step 5: Commit**

```
git add db/pool.go db/pool_test.go
git commit -m "feat(db): Pool wrapper recording dep=postgres metrics"
```

---

## Task 6 — pgx pool gauges

**Files:**
- Create: `obs/db_pool.go`
- Modify: `cmd/darek/chat.go` (or wherever `obs.Init` and `db.Open` are wired together) to register gauges after pool creation.

- [ ] **Step 1: Implement `RegisterPoolGauges`**

Create `obs/db_pool.go`:

```go
package obs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// PoolStatProvider is anything that exposes a *pgxpool.Stat. *db.Pool satisfies it.
type PoolStatProvider interface {
	Stat() *pgxpool.Stat
}

// RegisterPoolGauges registers async gauges that observe pool stats on every
// metric collection. Returns an error if the gauges can't be created.
func RegisterPoolGauges(p PoolStatProvider) error {
	m := otel.Meter("darek")
	acquired, err := m.Int64ObservableGauge("darek.db.pool.acquired")
	if err != nil {
		return fmt.Errorf("acquired gauge: %w", err)
	}
	idle, err := m.Int64ObservableGauge("darek.db.pool.idle")
	if err != nil {
		return fmt.Errorf("idle gauge: %w", err)
	}
	total, err := m.Int64ObservableGauge("darek.db.pool.total")
	if err != nil {
		return fmt.Errorf("total gauge: %w", err)
	}
	_, err = m.RegisterCallback(
		func(ctx context.Context, o metric.Observer) error {
			s := p.Stat()
			o.ObserveInt64(acquired, int64(s.AcquiredConns()))
			o.ObserveInt64(idle, int64(s.IdleConns()))
			o.ObserveInt64(total, int64(s.TotalConns()))
			return nil
		},
		acquired, idle, total,
	)
	if err != nil {
		return fmt.Errorf("register callback: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Wire into the CLI**

Find the place in `cmd/darek/` where `db.Open` is called (likely `chat.go` or a shared init). After the pool is opened, add:

```go
	if err := obs.RegisterPoolGauges(pool); err != nil {
		return fmt.Errorf("register pool gauges: %w", err)
	}
```

If the open returns errors, do this only on the success branch.

- [ ] **Step 3: Compile and test**

```
go build ./...
go test ./obs/... ./db/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add obs/db_pool.go cmd/darek/chat.go
git commit -m "feat(obs): pgx pool gauges (acquired, idle, total)"
```

---

## Task 7 — Migrate `memory` and `links` stores to use `*db.Pool`

**Files:**
- Modify: `memory/store.go`
- Modify: `memory/store_test.go`
- Modify: `links/store.go`
- Modify: `links/store_test.go`
- Modify: `cmd/darek/chat.go` (or wherever stores are constructed)

Both stores currently take `*pgxpool.Pool`. Change them to take `*db.Pool`. Also bump business counters in the right methods (covered here — saves a separate task).

- [ ] **Step 1: Modify `memory/store.go`**

Change the imports and struct:

```go
package memory

import (
	"context"
	"fmt"
	"time"

	"darek/db"
	"darek/obs"

	"github.com/google/uuid"
)

type Store struct {
	pool *db.Pool
	m    *obs.Metrics
}

func NewStore(pool *db.Pool) (*Store, error) {
	m, err := obs.MetricsInstance()
	if err != nil {
		return nil, fmt.Errorf("metrics: %w", err)
	}
	return &Store{pool: pool, m: m}, nil
}
```

In `Save`, after the successful insert, add:

```go
	s.m.MemoryNotesSaved.Add(ctx, 1)
```

In `Recall`, after `cur.Err()` is OK and we have results, add:

```go
	s.m.MemoryNotesRecalled.Add(ctx, int64(len(out)))
```

(So the counter reflects notes returned to the caller, not just calls — more useful for "how much memory was actually used".)

Drop the `*pgxpool.Pool` import.

- [ ] **Step 2: Update `memory/store_test.go`**

Wherever the test constructs `memory.NewStore(pool)`, change it to use a `*db.Pool`:

```go
	pool, err := db.Open(ctx, dsn)
	if err != nil { t.Fatalf("open: %v", err) }
	defer pool.Close()
	store, err := memory.NewStore(pool)
	if err != nil { t.Fatalf("new store: %v", err) }
```

`NewStore` now returns an error, so update the call sites accordingly.

- [ ] **Step 3: Modify `links/store.go`**

Same shape as `memory`:

```go
type Store struct {
	pool *db.Pool
	m    *obs.Metrics
}

func NewStore(pool *db.Pool) (*Store, error) {
	m, err := obs.MetricsInstance()
	if err != nil {
		return nil, fmt.Errorf("metrics: %w", err)
	}
	return &Store{pool: pool, m: m}, nil
}
```

`Save` already knows whether it's inserting or updating — bump `LinksEvents` accordingly. After the `INSERT` succeeds (the `errors.Is(err, pgx.ErrNoRows)` branch):

```go
		s.m.LinksEvents.Add(ctx, 1, metric.WithAttributes(attribute.String("op", "save_new")))
```

After the `UPDATE` succeeds:

```go
		s.m.LinksEvents.Add(ctx, 1, metric.WithAttributes(attribute.String("op", "save_update")))
```

In `Search`, before returning successfully:

```go
	s.m.LinksEvents.Add(ctx, 1, metric.WithAttributes(attribute.String("op", "search")))
```

In `Similar`, same:

```go
	s.m.LinksEvents.Add(ctx, 1, metric.WithAttributes(attribute.String("op", "similar")))
```

Required imports:

```go
import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)
```

The internal `tx.Begin / tx.QueryRow / tx.Exec` calls inside `Save` continue to use the `pgx.Tx` returned by `db.Pool.Begin` — those still work because pgx.Tx is the standard pgx type.

**Pitfall:** `Save` calls `s.pool.Begin(ctx)` — that's now `*db.Pool.Begin`, which records `op=tx_begin`. The internal `tx.QueryRow` / `tx.Exec` go straight through pgx (no wrapper metric). That's the documented tradeoff in `db/pool.go`.

- [ ] **Step 4: Update `links/store_test.go`**

Same change as memory — switch from `*pgxpool.Pool` to `*db.Pool`, update the `NewStore` signature.

- [ ] **Step 5: Update `cmd/darek/chat.go`**

The store constructors now return `(*Store, error)` and take `*db.Pool`. Update the wiring:

```go
	memStore, err := memory.NewStore(pool)
	if err != nil {
		return fmt.Errorf("memory store: %w", err)
	}
	linksStore, err := links.NewStore(pool)
	if err != nil {
		return fmt.Errorf("links store: %w", err)
	}
```

Replace the existing `memory.NewStore(pool)` and `links.NewStore(pool)` calls.

- [ ] **Step 6: Run all tests**

```
go test ./...
make test-integration
```

Expected: PASS.

- [ ] **Step 7: Commit**

```
git add memory/store.go memory/store_test.go links/store.go links/store_test.go cmd/darek/chat.go
git commit -m "feat(memory,links): use *db.Pool wrapper, bump activity counters"
```

---

## Task 8 — LLM client: wrap with `obs.Dep`

**Files:**
- Modify: `llm/client.go`

Today `Chat` records `LLMLatency` itself. We keep that (for the `model` dimension) and *also* call through `obs.Dep` for the uniform shape.

- [ ] **Step 1: Replace `Chat` body**

Current body wraps the OpenAI call with a span and records LLM-specific metrics. Reorganize so the OpenAI call runs inside `obs.Dep`:

```go
func (cl *Client) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	ctx, cancel := context.WithTimeout(ctx, cl.timeout)
	defer cancel()

	params.Model = cl.model
	start := time.Now()
	var resp *openai.ChatCompletion
	depErr := obs.Dep(ctx, "openai_chat", "chat", func(ctx context.Context) error {
		var err error
		resp, err = cl.c.Chat.Completions.New(ctx, params)
		return err
	})
	dur := time.Since(start).Seconds()

	outcome := "ok"
	if depErr != nil {
		outcome = "error"
	}
	cl.m.LLMLatency.Record(ctx, dur,
		metric.WithAttributes(attribute.String("model", cl.model), attribute.String("outcome", outcome)),
	)
	if depErr != nil {
		return nil, fmt.Errorf("openai chat: %w", depErr)
	}

	in := int(resp.Usage.PromptTokens)
	out := int(resp.Usage.CompletionTokens)
	cached := int(resp.Usage.PromptTokensDetails.CachedTokens)
	cost := Cost(cl.model, in, out, cached)

	mAttr := metric.WithAttributes(attribute.String("model", cl.model))
	cl.m.TokensInput.Add(ctx, int64(in), mAttr)
	cl.m.TokensOutput.Add(ctx, int64(out), mAttr)
	cl.m.TokensCached.Add(ctx, int64(cached), mAttr)
	cl.m.LLMCostUSD.Add(ctx, cost, mAttr)
	return resp, nil
}
```

The previous span (`cl.tracer.Start(ctx, "chat", ...)`) is now produced inside `obs.Dep`. Remove the manual `tracer.Start`, `span.RecordError`, `span.SetStatus`, and `span.SetAttributes` calls — `obs.Dep` covers them. Drop the unused `cl.tracer` field if no other method uses it; otherwise leave it.

- [ ] **Step 2: Verify imports**

The file already imports `darek/obs`, `metric`, `attribute`. Drop unused imports if any (`codes`, possibly `trace`).

- [ ] **Step 3: Run unit tests**

```
go test ./llm/...
```

Expected: PASS. Existing tests were against the old `Chat` shape which doesn't change observably.

- [ ] **Step 4: Commit**

```
git add llm/client.go
git commit -m "feat(llm): wrap Chat with obs.Dep(openai_chat)"
```

---

## Task 9 — Agent: bump `max_iters_hit`

**Files:**
- Modify: `agent/agent.go`

In `RunTurn`, the existing `if iters == a.maxIters` branch returns an error without recording metrics. Add a counter bump before returning.

- [ ] **Step 1: Modify the max-iters branch**

Replace this block (around line 115):

```go
	if iters == a.maxIters {
		err := fmt.Errorf("hit max iterations (%d) without final answer", a.maxIters)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
```

with:

```go
	if iters == a.maxIters {
		a.m.AgentMaxItersHit.Add(ctx, 1)
		err := fmt.Errorf("hit max iterations (%d) without final answer", a.maxIters)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
```

- [ ] **Step 2: Compile + run agent tests**

```
go test ./agent/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```
git add agent/agent.go
git commit -m "feat(agent): bump darek.agent.max_iters_hit on iter exhaustion"
```

---

## Task 10 — Calendar (Google) wrap

**Files:**
- Modify: `tools/calendar/google/google.go`

- [ ] **Step 1: Wrap `ListEvents`**

The current method does `call.Do()` to fetch events. Wrap that single call:

```go
func (s *Source) ListEvents(ctx context.Context, from, to time.Time) ([]calendar.Event, error) {
	tok, err := s.store.Load(s.nickname)
	if err != nil {
		return nil, fmt.Errorf("load token: %w", err)
	}
	httpClient := s.cfg.Client(ctx, tok)
	httpClient.Transport = otelhttp.NewTransport(httpClient.Transport)
	svc, err := calsvc.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("calendar svc: %w", err)
	}
	call := svc.Events.List(s.calID).
		SingleEvents(true).
		OrderBy("startTime").
		Context(ctx)
	if !from.IsZero() {
		call = call.TimeMin(from.Format(time.RFC3339))
	}
	if !to.IsZero() {
		call = call.TimeMax(to.Format(time.RFC3339))
	}
	var res *calsvc.Events
	depErr := obs.Dep(ctx, "google_calendar", "list_events", func(ctx context.Context) error {
		var err error
		res, err = call.Do()
		return err
	})
	if depErr != nil {
		return nil, fmt.Errorf("events.list: %w", depErr)
	}
	out := make([]calendar.Event, 0, len(res.Items))
	for _, it := range res.Items {
		ev, ok := convert(s.nickname, it)
		if !ok {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}
```

Add `"darek/obs"` to imports.

- [ ] **Step 2: Run tests**

```
go test ./tools/calendar/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```
git add tools/calendar/google/google.go
git commit -m "feat(calendar/google): wrap list_events with obs.Dep"
```

---

## Task 11 — Calendar (iCal) wrap

**Files:**
- Modify: `tools/calendar/ical/ical.go`

- [ ] **Step 1: Wrap the HTTP fetch**

Replace the request/response section in `ListEvents`:

```go
func (s *Source) ListEvents(ctx context.Context, from, to time.Time) ([]calendar.Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	var resp *http.Response
	depErr := obs.Dep(ctx, "ical", "fetch", func(ctx context.Context) error {
		var err error
		resp, err = s.client.Do(req)
		if err == nil && resp.StatusCode/100 != 2 {
			err = fmt.Errorf("status %d", resp.StatusCode)
			_ = resp.Body.Close()
			resp = nil
		}
		return err
	})
	if depErr != nil {
		return nil, fmt.Errorf("fetch %s: %w", s.url, depErr)
	}
	defer resp.Body.Close()
	cal, err := ics.ParseCalendar(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse ics: %w", err)
	}
	// ... existing filtering loop unchanged
```

Add `"darek/obs"` to imports. The existing `if resp.StatusCode/100 != 2` block is folded into the closure so the dep `outcome` reflects HTTP status correctly.

- [ ] **Step 2: Run tests**

```
go test ./tools/calendar/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```
git add tools/calendar/ical/ical.go
git commit -m "feat(calendar/ical): wrap fetch with obs.Dep"
```

---

## Task 12 — Todoist client wrap

**Files:**
- Modify: `tools/todoist/client.go`

The client routes everything through a private `doJSON(ctx, method, path, body, out)` helper. Add an `op` parameter and wrap the network portion.

- [ ] **Step 1: Replace `doJSON`**

Replace the existing `doJSON` (currently at the end of `client.go`) with:

```go
func (c *Client) doJSON(ctx context.Context, op, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return obs.Dep(ctx, "todoist", op, func(ctx context.Context) error {
		resp, err := c.http.Do(req.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("http: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("todoist %s %s: status %d: %s", method, path, resp.StatusCode, string(b))
		}
		if out == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		return nil
	})
}
```

JSON-marshal stays outside the closure (it's local CPU). Network + decode are inside.

- [ ] **Step 2: Update each call site**

In the same file:

- `ListTasks` → change `c.doJSON(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &env)` to `c.doJSON(ctx, "list_tasks", http.MethodGet, path+"?"+q.Encode(), nil, &env)`.
- `CreateTask` → `c.doJSON(ctx, "create_task", http.MethodPost, "/tasks", req, &out)`.
- `CompleteTask` → `c.doJSON(ctx, "complete_task", http.MethodPost, "/tasks/"+id+"/close", nil, nil)`.
- `UpdateTask` → `c.doJSON(ctx, "update_task", http.MethodPost, "/tasks/"+id, req, &out)`.

Add `"darek/obs"` to the imports.

- [ ] **Step 2: Run tests**

```
go test ./tools/todoist/...
```

Expected: PASS. The mock-server tests don't care about the dep wrapper.

- [ ] **Step 3: Commit**

```
git add tools/todoist/client.go
git commit -m "feat(todoist): wrap doJSON with obs.Dep, op per method"
```

---

## Task 13 — FreshRSS client wrap

**Files:**
- Modify: `tools/freshrss/client.go`

Wrap `login`, `List`, `Get`, `Mark`, and `editToken` (separately so each has a meaningful op).

- [ ] **Step 1: Wrap each method**

For `List`, replace the `c.authedDo(ctx, req)` call (and the body-read + parse — the parse is decoding, not network, so keep parse out of the closure):

```go
	var resp *http.Response
	depErr := obs.Dep(ctx, "freshrss", "list", func(ctx context.Context) error {
		var err error
		resp, err = c.authedDo(ctx, req)
		if err == nil && resp.StatusCode/100 != 2 {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			err = fmt.Errorf("list status %d: %s", resp.StatusCode, string(body))
			resp = nil
		}
		return err
	})
	if depErr != nil {
		return nil, depErr
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// ... existing parse
```

Apply the same pattern to `Get` (op `"get"`), `Mark` (op `"mark"`), and `login` (op `"login"`). For `editToken`, use op `"login"` as well (it's part of the auth flow), or `"token"` if we want to track separately — pick `"token"` for clarity.

Update the cardinality test allowed list if you pick a different name than `login/list/get/mark/token`.

Add `"darek/obs"` to imports.

- [ ] **Step 2: Run tests**

```
go test ./tools/freshrss/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```
git add tools/freshrss/client.go
git commit -m "feat(freshrss): wrap HTTP calls with obs.Dep, op per method"
```

---

## Task 14 — IMAP wrap + business counters

**Files:**
- Modify: `tools/mail/imap/imap.go`
- Modify: `tools/mail/imap/append.go`

The existing methods `SyncFolder`, `FetchBody`, `FetchAttachment`, and `Append` open an explicit span, do the work, and close the span. Replace with `obs.Dep`.

- [ ] **Step 1: Rewrite `SyncFolder`**

Replace the entire function body with:

```go
func (a *Account) SyncFolder(ctx context.Context, folder string, sinceUID uint32) ([]mail.Envelope, uint32, error) {
	var envs []mail.Envelope
	var uidValidity uint32
	depErr := obs.Dep(ctx, "imap", "sync_folder", func(ctx context.Context) error {
		c, err := a.connect(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = c.Logout().Wait() }()

		mb, err := c.Select(folder, &goimap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return fmt.Errorf("select %s: %w", folder, err)
		}
		uidValidity = mb.UIDValidity
		if mb.NumMessages == 0 {
			return nil
		}

		var seqset goimap.UIDSet
		seqset.AddRange(goimap.UID(sinceUID+1), 0)
		fetchOpts := &goimap.FetchOptions{
			Envelope:      true,
			Flags:         true,
			InternalDate:  true,
			BodyStructure: &goimap.FetchItemBodyStructure{Extended: true},
			UID:           true,
		}
		cmd := c.Fetch(seqset, fetchOpts)
		defer cmd.Close()

		for {
			msg := cmd.Next()
			if msg == nil {
				break
			}
			buf, err := msg.Collect()
			if err != nil {
				return fmt.Errorf("collect msg: %w", err)
			}
			envs = append(envs, fromGoimap(buf))
		}
		if err := cmd.Close(); err != nil {
			return fmt.Errorf("fetch close: %w", err)
		}
		enrichSnippets(c, &envs)
		return nil
	})
	if depErr != nil {
		return nil, 0, depErr
	}
	m, _ := obs.MetricsInstance()
	m.MailEnvelopesSynced.Add(ctx, int64(len(envs)),
		metric.WithAttributes(attribute.String("account", a.nickname)))
	return envs, uidValidity, nil
}
```

Drop `a.tracer` usage in this method — `obs.Dep` covers span and error recording. Remove the now-unused `codes`/`trace` imports if no other method in the file references them (the `attribute` import stays — it's still used for the account label).

- [ ] **Step 2: Rewrite `FetchBody`**

```go
func (a *Account) FetchBody(ctx context.Context, folder string, uid uint32) (string, error) {
	var body string
	depErr := obs.Dep(ctx, "imap", "fetch_body", func(ctx context.Context) error {
		c, err := a.connect(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = c.Logout().Wait() }()
		if _, err := c.Select(folder, &goimap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("select: %w", err)
		}
		var us goimap.UIDSet
		us.AddNum(goimap.UID(uid))
		textSection := &goimap.FetchItemBodySection{Specifier: goimap.PartSpecifierText}
		cmd := c.Fetch(us, &goimap.FetchOptions{
			UID:         true,
			BodySection: []*goimap.FetchItemBodySection{textSection},
		})
		defer cmd.Close()
		msg := cmd.Next()
		if msg == nil {
			return fmt.Errorf("uid %d not found", uid)
		}
		buf, err := msg.Collect()
		if err != nil {
			return err
		}
		for _, b := range buf.BodySection {
			body = string(b.Bytes)
			return nil
		}
		return fmt.Errorf("no body section returned")
	})
	if depErr != nil {
		return "", depErr
	}
	m, _ := obs.MetricsInstance()
	m.MailBodiesFetched.Add(ctx, 1, metric.WithAttributes(attribute.String("account", a.nickname)))
	return body, nil
}
```

- [ ] **Step 3: Rewrite `FetchAttachment`**

The existing implementation reads the entire attachment into memory and returns `io.NopCloser(strings.NewReader(...))` after logging out, so the connection is *not* tied to the returned reader. That makes the wrapping straightforward — capture `rc` and return it after the closure ends.

```go
func (a *Account) FetchAttachment(ctx context.Context, folder string, uid uint32, partID string) (io.ReadCloser, error) {
	var rc io.ReadCloser
	depErr := obs.Dep(ctx, "imap", "fetch_attachment", func(ctx context.Context) error {
		c, err := a.connect(ctx)
		if err != nil {
			return err
		}
		if _, err := c.Select(folder, &goimap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			_ = c.Close()
			return fmt.Errorf("select: %w", err)
		}
		var us goimap.UIDSet
		us.AddNum(goimap.UID(uid))
		partSection := &goimap.FetchItemBodySection{
			Specifier: goimap.PartSpecifierNone,
			Part:      parsePartID(partID),
		}
		cmd := c.Fetch(us, &goimap.FetchOptions{
			UID:         true,
			BodySection: []*goimap.FetchItemBodySection{partSection},
		})
		msg := cmd.Next()
		if msg == nil {
			_ = cmd.Close()
			_ = c.Close()
			return fmt.Errorf("uid %d not found", uid)
		}
		buf, err := msg.Collect()
		_ = cmd.Close()
		if err != nil {
			_ = c.Close()
			return err
		}
		for _, b := range buf.BodySection {
			_ = c.Logout().Wait()
			rc = io.NopCloser(strings.NewReader(string(b.Bytes)))
			return nil
		}
		_ = c.Close()
		return fmt.Errorf("no body section returned")
	})
	if depErr != nil {
		return nil, depErr
	}
	m, _ := obs.MetricsInstance()
	m.MailAttachmentsFetched.Add(ctx, 1, metric.WithAttributes(attribute.String("account", a.nickname)))
	return rc, nil
}
```

- [ ] **Step 4: Rewrite `Append` in `append.go`**

The current `Append` takes `(ctx, folder, flags, raw)`. Replace its body so the entire IMAP transaction runs inside `obs.Dep`:

```go
func (a *Account) Append(ctx context.Context, folder string, flags []string, raw []byte) error {
	return obs.Dep(ctx, "imap", "append", func(ctx context.Context) error {
		c, err := a.connect(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = c.Logout().Wait() }()

		gflags := make([]goimap.Flag, len(flags))
		for i, f := range flags {
			gflags[i] = goimap.Flag(f)
		}

		cmd := c.Append(folder, int64(len(raw)), &goimap.AppendOptions{
			Time:  time.Now(),
			Flags: gflags,
		})
		if _, err := cmd.Write(raw); err != nil {
			_ = cmd.Close()
			return fmt.Errorf("append write: %w", err)
		}
		if err := cmd.Close(); err != nil {
			return fmt.Errorf("append close: %w", err)
		}
		if _, err := cmd.Wait(); err != nil {
			return fmt.Errorf("append wait: %w", err)
		}
		return nil
	})
}
```

Drop the manual `tracer.Start` / `span.RecordError` / `span.SetStatus` lines and the now-unused `codes`/`trace`/`attribute` imports (keep `attribute` only if other code in the file uses it).

The `MailSent` counter is bumped at the SMTP layer (Task 15), not here.

- [ ] **Step 5: Imports**

Add `"darek/obs"` and `"go.opentelemetry.io/otel/attribute"` and `"go.opentelemetry.io/otel/metric"` to both files. Remove unused imports (`codes`, `trace` if no other span code remains in the file).

- [ ] **Step 6: Run tests**

```
go test ./tools/mail/...
make test-integration
```

Expected: PASS.

- [ ] **Step 7: Commit**

```
git add tools/mail/imap/imap.go tools/mail/imap/append.go
git commit -m "feat(imap): wrap with obs.Dep + bump mail.{envelopes_synced,bodies_fetched,attachments_fetched}"
```

---

## Task 15 — SMTP wrap + `mail.sent`

**Files:**
- Modify: `tools/mail/smtp/smtp.go`

- [ ] **Step 1: Replace `Send`**

```go
func (s *Sender) Send(ctx context.Context, from string, recipients []string, raw []byte) error {
	depErr := obs.Dep(ctx, "smtp", "send", func(ctx context.Context) error {
		addr := net.JoinHostPort(s.opts.Host, fmt.Sprint(s.opts.Port))
		dialer := &net.Dialer{Timeout: s.opts.Timeout}
		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			return fmt.Errorf("smtp dial %s: %w", addr, err)
		}

		var c *smtp.Client
		if s.opts.TLS {
			tlsConn := tls.Client(conn, &tls.Config{ServerName: s.opts.Host})
			if err := tlsConn.Handshake(); err != nil {
				_ = conn.Close()
				return fmt.Errorf("tls handshake: %w", err)
			}
			c, err = smtp.NewClient(tlsConn, s.opts.Host)
		} else {
			c, err = smtp.NewClient(conn, s.opts.Host)
		}
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("smtp new client: %w", err)
		}
		defer func() { _ = c.Quit() }()

		if !s.opts.TLS {
			if ok, _ := c.Extension("STARTTLS"); ok {
				if err := c.StartTLS(&tls.Config{ServerName: s.opts.Host}); err != nil {
					return fmt.Errorf("starttls: %w", err)
				}
			}
		}
		if s.opts.Username != "" {
			auth := smtp.PlainAuth("", s.opts.Username, s.opts.Password, s.opts.Host)
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
		if err := c.Mail(from); err != nil {
			return fmt.Errorf("MAIL FROM: %w", err)
		}
		for _, r := range recipients {
			if err := c.Rcpt(r); err != nil {
				return fmt.Errorf("RCPT TO %s: %w", r, err)
			}
		}
		w, err := c.Data()
		if err != nil {
			return fmt.Errorf("DATA: %w", err)
		}
		if _, err := w.Write(raw); err != nil {
			return fmt.Errorf("write body: %w", err)
		}
		if err := w.Close(); err != nil {
			return fmt.Errorf("close data writer: %w", err)
		}
		return nil
	})
	if depErr != nil {
		return depErr
	}
	m, _ := obs.MetricsInstance()
	m.MailSent.Add(ctx, 1)
	return nil
}
```

The previous span (`s.tracer.Start`) and per-step `span.RecordError`/`span.SetStatus` lines are gone — `obs.Dep` covers them. Drop the unused imports (`go.opentelemetry.io/otel`, `codes`, `trace`, `attribute`); keep only what `Send` actually uses (`darek/obs`, `crypto/tls`, `fmt`, `net`, `net/smtp`, `time`).

The `MailSent` counter has no `account` label here — the SMTP sender doesn't know the nickname. If the calling code does, it can pass it in via constructor option in a follow-up.

- [ ] **Step 2: Imports**

Add `"darek/obs"`. Remove `codes`, `trace`, `attribute` if no longer used.

- [ ] **Step 3: Run tests**

```
go test ./tools/mail/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add tools/mail/smtp/smtp.go
git commit -m "feat(smtp): wrap Send with obs.Dep + bump mail.sent"
```

---

## Task 16 — Refresh `agent_turns` and `tokens_and_cost` dashboards

**Files:**
- Modify: `otel/grafana/dashboards/agent_turns.json`
- Modify: `otel/grafana/dashboards/tokens_and_cost.json`

- [ ] **Step 1: Replace `agent_turns.json`**

```json
{
  "title": "darek — agent turns",
  "schemaVersion": 39,
  "version": 2,
  "panels": [
    {
      "type": "stat",
      "title": "Turns/min",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(rate(darek_turn_duration_seconds_count[1m]))"}],
      "gridPos": {"h": 5, "w": 6, "x": 0, "y": 0}
    },
    {
      "type": "stat",
      "title": "Max-iters hit (1h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_agent_max_iters_hit_total[1h]))"}],
      "gridPos": {"h": 5, "w": 6, "x": 6, "y": 0}
    },
    {
      "type": "stat",
      "title": "Turn error rate (5m)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(rate(darek_turn_duration_seconds_count{outcome=\"error\"}[5m])) / clamp_min(sum(rate(darek_turn_duration_seconds_count[5m])), 1e-9)"}],
      "gridPos": {"h": 5, "w": 6, "x": 12, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Turn duration p50/p95",
      "datasource": "Prometheus",
      "targets": [
        {"expr": "histogram_quantile(0.50, sum by (le) (rate(darek_turn_duration_seconds_bucket[5m])))", "legendFormat": "p50"},
        {"expr": "histogram_quantile(0.95, sum by (le) (rate(darek_turn_duration_seconds_bucket[5m])))", "legendFormat": "p95"}
      ],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 5}
    },
    {
      "type": "timeseries",
      "title": "Iterations per turn (p95)",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.95, sum by (le) (rate(darek_turn_iterations_bucket[5m])))"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 5}
    },
    {
      "type": "timeseries",
      "title": "Turns by outcome",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (outcome) (rate(darek_turn_duration_seconds_count[1m]))", "legendFormat": "{{outcome}}"}],
      "gridPos": {"h": 8, "w": 24, "x": 0, "y": 13}
    }
  ]
}
```

- [ ] **Step 2: Replace `tokens_and_cost.json`**

```json
{
  "title": "darek — tokens & cost",
  "schemaVersion": 39,
  "version": 2,
  "panels": [
    {
      "type": "timeseries",
      "title": "Tokens/s by kind",
      "datasource": "Prometheus",
      "targets": [
        {"expr": "sum(rate(darek_tokens_input_total[1m]))",  "legendFormat": "input"},
        {"expr": "sum(rate(darek_tokens_output_total[1m]))", "legendFormat": "output"},
        {"expr": "sum(rate(darek_tokens_cached_total[1m]))", "legendFormat": "cached"}
      ],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Tokens/s by model (input)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (model) (rate(darek_tokens_input_total[1m]))"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 0}
    },
    {
      "type": "stat",
      "title": "USD spent (1h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_llm_cost_usd_total[1h]))"}],
      "gridPos": {"h": 5, "w": 6, "x": 0, "y": 8}
    },
    {
      "type": "stat",
      "title": "Cache hit %",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(rate(darek_tokens_cached_total[5m])) / clamp_min(sum(rate(darek_tokens_input_total[5m])), 1e-9)"}],
      "gridPos": {"h": 5, "w": 6, "x": 6, "y": 8}
    },
    {
      "type": "timeseries",
      "title": "Cost rate by model",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (model) (rate(darek_llm_cost_usd_total[5m]))"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 8}
    }
  ]
}
```

- [ ] **Step 3: Commit**

```
git add otel/grafana/dashboards/agent_turns.json otel/grafana/dashboards/tokens_and_cost.json
git commit -m "chore(grafana): refresh agent_turns + tokens_and_cost dashboards"
```

---

## Task 17 — Rename and refresh tools dashboard

**Files:**
- Delete: `otel/grafana/dashboards/tool_latency.json`
- Create: `otel/grafana/dashboards/tools.json`

- [ ] **Step 1: Delete the old dashboard**

```
git rm otel/grafana/dashboards/tool_latency.json
```

- [ ] **Step 2: Create `tools.json`**

```json
{
  "title": "darek — tools",
  "schemaVersion": 39,
  "version": 1,
  "panels": [
    {
      "type": "timeseries",
      "title": "Tool calls/s by name",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (tool) (rate(darek_tool_calls_total[1m]))"}],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Tool error rate by name",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (tool) (rate(darek_tool_calls_total{outcome=\"error\"}[5m])) / clamp_min(sum by (tool) (rate(darek_tool_calls_total[5m])), 1e-9)"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Tool latency p50",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.50, sum by (le, tool) (rate(darek_tool_latency_seconds_bucket[5m])))"}],
      "gridPos": {"h": 8, "w": 8, "x": 0, "y": 8}
    },
    {
      "type": "timeseries",
      "title": "Tool latency p95",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.95, sum by (le, tool) (rate(darek_tool_latency_seconds_bucket[5m])))"}],
      "gridPos": {"h": 8, "w": 8, "x": 8, "y": 8}
    },
    {
      "type": "timeseries",
      "title": "Tool latency p99",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.99, sum by (le, tool) (rate(darek_tool_latency_seconds_bucket[5m])))"}],
      "gridPos": {"h": 8, "w": 8, "x": 16, "y": 8}
    }
  ]
}
```

- [ ] **Step 3: Commit**

```
git add otel/grafana/dashboards/tools.json
git commit -m "chore(grafana): rename tool_latency → tools, add error rate + p50/p99"
```

---

## Task 18 — New `external_deps` dashboard

**Files:**
- Create: `otel/grafana/dashboards/external_deps.json`

- [ ] **Step 1: Create the file**

```json
{
  "title": "darek — external deps",
  "schemaVersion": 39,
  "version": 1,
  "panels": [
    {
      "type": "timeseries",
      "title": "RPS by dep",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (dep) (rate(darek_dep_requests_total[1m]))"}],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Error rate by dep",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (dep) (rate(darek_dep_requests_total{outcome=\"error\"}[5m])) / clamp_min(sum by (dep) (rate(darek_dep_requests_total[5m])), 1e-9)"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Latency p50 by dep",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.50, sum by (le, dep) (rate(darek_dep_latency_seconds_bucket[5m])))"}],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 8}
    },
    {
      "type": "timeseries",
      "title": "Latency p95 by dep",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.95, sum by (le, dep) (rate(darek_dep_latency_seconds_bucket[5m])))"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 8}
    },
    {
      "type": "table",
      "title": "Top slow ops (p95)",
      "datasource": "Prometheus",
      "targets": [{"expr": "topk(10, histogram_quantile(0.95, sum by (le, dep, op) (rate(darek_dep_latency_seconds_bucket[5m]))))", "format": "table", "instant": true}],
      "gridPos": {"h": 8, "w": 24, "x": 0, "y": 16}
    }
  ]
}
```

- [ ] **Step 2: Commit**

```
git add otel/grafana/dashboards/external_deps.json
git commit -m "chore(grafana): new external_deps dashboard"
```

---

## Task 19 — New `mail` dashboard

**Files:**
- Create: `otel/grafana/dashboards/mail.json`

- [ ] **Step 1: Create the file**

```json
{
  "title": "darek — mail",
  "schemaVersion": 39,
  "version": 1,
  "panels": [
    {
      "type": "stat",
      "title": "Envelopes synced (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_mail_envelopes_synced_total[24h]))"}],
      "gridPos": {"h": 5, "w": 6, "x": 0, "y": 0}
    },
    {
      "type": "stat",
      "title": "Bodies fetched (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_mail_bodies_fetched_total[24h]))"}],
      "gridPos": {"h": 5, "w": 6, "x": 6, "y": 0}
    },
    {
      "type": "stat",
      "title": "Attachments fetched (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_mail_attachments_fetched_total[24h]))"}],
      "gridPos": {"h": 5, "w": 6, "x": 12, "y": 0}
    },
    {
      "type": "stat",
      "title": "Mails sent (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_mail_sent_total[24h]))"}],
      "gridPos": {"h": 5, "w": 6, "x": 18, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Envelopes synced/min by account",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (account) (rate(darek_mail_envelopes_synced_total[1m]))"}],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 5}
    },
    {
      "type": "timeseries",
      "title": "IMAP error rate by op",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (op) (rate(darek_dep_requests_total{dep=\"imap\",outcome=\"error\"}[5m])) / clamp_min(sum by (op) (rate(darek_dep_requests_total{dep=\"imap\"}[5m])), 1e-9)"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 5}
    },
    {
      "type": "timeseries",
      "title": "IMAP sync_folder p95",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.95, sum by (le) (rate(darek_dep_latency_seconds_bucket{dep=\"imap\",op=\"sync_folder\"}[5m])))"}],
      "gridPos": {"h": 8, "w": 24, "x": 0, "y": 13}
    }
  ]
}
```

- [ ] **Step 2: Commit**

```
git add otel/grafana/dashboards/mail.json
git commit -m "chore(grafana): new mail dashboard"
```

---

## Task 20 — New `links_memory` dashboard

**Files:**
- Create: `otel/grafana/dashboards/links_memory.json`

- [ ] **Step 1: Create the file**

```json
{
  "title": "darek — links & memory",
  "schemaVersion": 39,
  "version": 1,
  "panels": [
    {
      "type": "timeseries",
      "title": "Links events/min by op",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (op) (rate(darek_links_events_total[1m]))"}],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 0}
    },
    {
      "type": "stat",
      "title": "Links saved (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_links_events_total{op=~\"save_new|save_update\"}[24h]))"}],
      "gridPos": {"h": 5, "w": 6, "x": 12, "y": 0}
    },
    {
      "type": "stat",
      "title": "New unique links (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_links_events_total{op=\"save_new\"}[24h]))"}],
      "gridPos": {"h": 5, "w": 6, "x": 18, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Notes saved/recalled per minute",
      "datasource": "Prometheus",
      "targets": [
        {"expr": "sum(rate(darek_memory_notes_saved_total[1m]))",   "legendFormat": "saved"},
        {"expr": "sum(rate(darek_memory_notes_recalled_total[1m]))","legendFormat": "recalled"}
      ],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 8}
    },
    {
      "type": "timeseries",
      "title": "Postgres p95 by op",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.95, sum by (le, op) (rate(darek_dep_latency_seconds_bucket{dep=\"postgres\"}[5m])))"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 8}
    }
  ]
}
```

- [ ] **Step 2: Commit**

```
git add otel/grafana/dashboards/links_memory.json
git commit -m "chore(grafana): new links_memory dashboard"
```

---

## Task 21 — New `runtime` dashboard

**Files:**
- Create: `otel/grafana/dashboards/runtime.json`

The metric names emitted by `otelruntime` follow OTel semantic conventions. They get translated by the prometheus exporter into names like `process_runtime_go_goroutines`, `process_runtime_go_gc_pause_ns_*`, `process_runtime_go_mem_heap_alloc_bytes`. Names depend on the contrib version. After the implementation lands, run `curl :8889/metrics | grep -i runtime` against the OTel collector's prometheus endpoint and substitute the actual metric names if these don't match.

- [ ] **Step 1: Create the file**

```json
{
  "title": "darek — runtime",
  "schemaVersion": 39,
  "version": 1,
  "panels": [
    {
      "type": "timeseries",
      "title": "Goroutines",
      "datasource": "Prometheus",
      "targets": [{"expr": "process_runtime_go_goroutines"}],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Heap in-use bytes",
      "datasource": "Prometheus",
      "targets": [{"expr": "process_runtime_go_mem_heap_alloc_bytes"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "GC pause p95 (ns)",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.95, sum by (le) (rate(process_runtime_go_gc_pause_ns_bucket[5m])))"}],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 8}
    },
    {
      "type": "timeseries",
      "title": "pgx pool",
      "datasource": "Prometheus",
      "targets": [
        {"expr": "darek_db_pool_acquired", "legendFormat": "acquired"},
        {"expr": "darek_db_pool_idle",     "legendFormat": "idle"},
        {"expr": "darek_db_pool_total",    "legendFormat": "total"}
      ],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 8}
    },
    {
      "type": "timeseries",
      "title": "DB query p95 by op",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.95, sum by (le, op) (rate(darek_dep_latency_seconds_bucket{dep=\"postgres\"}[5m])))"}],
      "gridPos": {"h": 8, "w": 24, "x": 0, "y": 16}
    }
  ]
}
```

- [ ] **Step 2: Commit**

```
git add otel/grafana/dashboards/runtime.json
git commit -m "chore(grafana): new runtime dashboard"
```

---

## Task 22 — New `overview` dashboard

**Files:**
- Create: `otel/grafana/dashboards/overview.json`

- [ ] **Step 1: Create the file**

```json
{
  "title": "darek — overview",
  "schemaVersion": 39,
  "version": 1,
  "links": [
    {"title": "agent turns",  "type": "link", "url": "/d/agent_turns"},
    {"title": "tokens & cost","type": "link", "url": "/d/tokens_and_cost"},
    {"title": "tools",        "type": "link", "url": "/d/tools"},
    {"title": "external deps","type": "link", "url": "/d/external_deps"},
    {"title": "mail",         "type": "link", "url": "/d/mail"},
    {"title": "links & memory","type": "link","url": "/d/links_memory"},
    {"title": "runtime",      "type": "link", "url": "/d/runtime"}
  ],
  "panels": [
    {
      "type": "stat",
      "title": "Turns (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_turn_duration_seconds_count[24h]))"}],
      "gridPos": {"h": 5, "w": 5, "x": 0, "y": 0}
    },
    {
      "type": "stat",
      "title": "Tokens (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_tokens_input_total[24h])) + sum(increase(darek_tokens_output_total[24h]))"}],
      "gridPos": {"h": 5, "w": 5, "x": 5, "y": 0}
    },
    {
      "type": "stat",
      "title": "USD (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_llm_cost_usd_total[24h]))"}],
      "gridPos": {"h": 5, "w": 4, "x": 10, "y": 0}
    },
    {
      "type": "stat",
      "title": "Error rate (1h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(rate(darek_dep_requests_total{outcome=\"error\"}[1h])) / clamp_min(sum(rate(darek_dep_requests_total[1h])), 1e-9)"}],
      "gridPos": {"h": 5, "w": 5, "x": 14, "y": 0}
    },
    {
      "type": "stat",
      "title": "Goroutines",
      "datasource": "Prometheus",
      "targets": [{"expr": "process_runtime_go_goroutines"}],
      "gridPos": {"h": 5, "w": 5, "x": 19, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Tool calls/min (top 10)",
      "datasource": "Prometheus",
      "targets": [{"expr": "topk(10, sum by (tool) (rate(darek_tool_calls_total[1m])))"}],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 5}
    },
    {
      "type": "stat",
      "title": "Mail synced (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_mail_envelopes_synced_total[24h]))"}],
      "gridPos": {"h": 4, "w": 4, "x": 12, "y": 5}
    },
    {
      "type": "stat",
      "title": "Links saved (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_links_events_total{op=~\"save_new|save_update\"}[24h]))"}],
      "gridPos": {"h": 4, "w": 4, "x": 16, "y": 5}
    },
    {
      "type": "stat",
      "title": "Notes saved (24h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_memory_notes_saved_total[24h]))"}],
      "gridPos": {"h": 4, "w": 4, "x": 20, "y": 5}
    },
    {
      "type": "timeseries",
      "title": "$/hour by model",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (model) (rate(darek_llm_cost_usd_total[1h]))"}],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 13}
    },
    {
      "type": "timeseries",
      "title": "Latency p95 — turn vs LLM vs deps",
      "datasource": "Prometheus",
      "targets": [
        {"expr": "histogram_quantile(0.95, sum by (le) (rate(darek_turn_duration_seconds_bucket[5m])))",     "legendFormat": "turn p95"},
        {"expr": "histogram_quantile(0.95, sum by (le) (rate(darek_llm_latency_seconds_bucket[5m])))",       "legendFormat": "llm p95"},
        {"expr": "histogram_quantile(0.95, sum by (le, dep) (rate(darek_dep_latency_seconds_bucket[5m])))",  "legendFormat": "{{dep}} p95"}
      ],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 13}
    },
    {
      "type": "timeseries",
      "title": "Errors/min by component",
      "datasource": "Prometheus",
      "targets": [
        {"expr": "sum(rate(darek_dep_requests_total{outcome=\"error\"}[1m]))",   "legendFormat": "dep"},
        {"expr": "sum(rate(darek_tool_calls_total{outcome=\"error\"}[1m]))",     "legendFormat": "tool"},
        {"expr": "sum(rate(darek_turn_duration_seconds_count{outcome=\"error\"}[1m]))", "legendFormat": "turn"},
        {"expr": "sum(rate(darek_agent_max_iters_hit_total[1m]))",               "legendFormat": "agent (max-iters)"}
      ],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 21}
    },
    {
      "type": "table",
      "title": "Top 5 failing ops (5m)",
      "datasource": "Prometheus",
      "targets": [{"expr": "topk(5, sum by (dep, op) (rate(darek_dep_requests_total{outcome=\"error\"}[5m])) / clamp_min(sum by (dep, op) (rate(darek_dep_requests_total[5m])), 1e-9))", "format": "table", "instant": true}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 21}
    }
  ]
}
```

- [ ] **Step 2: Commit**

```
git add otel/grafana/dashboards/overview.json
git commit -m "chore(grafana): new overview dashboard"
```

---

## Task 23 — Manual verification

This task isn't a code change; it's the smoke test. Document the result.

- [ ] **Step 1: Bring up the stack and the app**

```
make up
make obs-up
make build
```

- [ ] **Step 2: Generate traffic**

```
./darek "what trips am I tracking?"
./darek "save https://example.com — testing observability, rated 5, tags test"
./darek "find similar links to 'observability metrics dashboards'"
./darek mail sync         # if a mail account is configured
```

(Substitute commands if some integrations aren't configured; the goal is to hit at least the LLM path and one tool with a DB write/read.)

- [ ] **Step 3: Confirm in Grafana**

Open http://localhost:3000 → `darek` folder. Each of the seven dashboards should populate within ~30s of the last command:

- `darek — overview`: Turns (24h) ≥ 1, USD (24h) > 0, tool calls visible, latency panel populated.
- `darek — agent turns`: turn duration p50/p95 visible, "Turns by outcome" shows `ok` series.
- `darek — tokens & cost`: tokens/s by kind populated, USD spent > 0, cache hit % between 0 and 1.
- `darek — tools`: per-tool calls/s and latency visible for each tool you exercised.
- `darek — external deps`: `openai_chat` series visible; `postgres` series visible if a DB call ran.
- `darek — mail`: only populated if mail sync was run.
- `darek — links & memory`: events visible if a links tool ran.
- `darek — runtime`: goroutines > 0; pgx pool gauges visible after at least one DB call.

- [ ] **Step 4: Sanity-check the prometheus exporter**

```
curl -s http://localhost:8889/metrics | grep -E '^darek_' | sort -u | head
```

Expected: a line per metric (`darek_dep_requests_total`, `darek_dep_latency_seconds_*`, `darek_tokens_*`, etc.).

- [ ] **Step 5: Commit a verification note (optional)**

If you needed to adjust runtime metric names in `runtime.json` because the exporter renamed them, commit those edits:

```
git add otel/grafana/dashboards/runtime.json
git commit -m "chore(grafana): adjust runtime metric names to match exporter"
```

Otherwise the work is done — the dashboards are provisioned, the metrics flow on every invocation.

---

## Self-review notes

- All sections of the spec map to tasks: §3.1 → Tasks 5, 6, 8, 10–15; §3.2 → Tasks 1, 9; §3.3 → Tasks 7, 14, 15; §3.4 → Tasks 3, 6; §3.5 → Task 4; §4 → Tasks 16–22; §6 → Tasks 1, 2, 4, and verification in 23.
- Type consistency: `*db.Pool` introduced in Task 5 and used unchanged in Task 7.
- The `obs.Dep` signature is identical in every wrapping task.
- The `outcome` label is `ok` / `error` everywhere.
- One spec gap to flag: §6 mentions a `db.Querier` wrapper test for `tx_begin`. The integration test in Task 5 doesn't explicitly cover `tx_begin` — the worker should add a small unit test there if the existing pgx test infrastructure makes it cheap. If not, leave it; the wrapper code is small and the cardinality test catches the label shape.
