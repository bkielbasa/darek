# Darek — observability metrics & Grafana dashboards (design)

**Date:** 2026-04-29
**Status:** approved (awaiting implementation plan)
**Author:** brainstormed with Claude

## 1. Goal

Make darek fully observable from a Grafana browser tab: cost, activity, and health all visible at a glance, with drill-downs into LLM, tools, external dependencies, mail, links/memory, and runtime. Every network or process boundary should emit uniform `requests` + `latency` metrics so a slow tool can be split into "API was slow" vs "local code was slow".

The metrics layer already exists (OTEL → OTLP gRPC → Collector → Prometheus + Jaeger; provisioned Grafana). This work fills the gaps in instrumentation and replaces three small dashboards with a coherent set of seven.

## 2. Scope

### In

- New uniform external-dependency metrics across all 9 deps darek talks to.
- Business-activity counters for mail, memory, and links (the things not already covered by tool counters).
- Postgres pool gauges and a thin `db.Querier` wrapper that records query metrics.
- Go runtime instrumentation (goroutines, GC, heap).
- One overview dashboard, three refreshed dashboards, three new drill-down dashboards.
- Cardinality discipline rule and tests.

### Out (deferred)

- Alerts / Alertmanager rules.
- Long-term storage / retention tuning.
- Distributed-tracing improvements beyond what already exists.
- Log-based metrics.
- A "service mode" daemon — runtime metrics will become more useful there, but that's a separate plan.

## 3. Metric inventory

### 3.1 External dependencies (uniform shape)

Every call that crosses the network or process boundary records:

- `darek.dep.requests` — counter — labels: `dep`, `op`, `outcome`
- `darek.dep.latency` — histogram (seconds) — labels: `dep`, `op`, `outcome`

`dep` is one of: `openai_chat`, `google_calendar`, `todoist`, `freshrss`, `ical`, `imap`, `smtp`, `postgres`. (Embeddings aren't wired today — links uses tsvector. `openai_embeddings` will join when it's used.)
`op` is dep-specific (e.g., `imap`: `sync_folder` / `fetch_body` / `fetch_attachment` / `append`; `postgres`: `query` / `exec` / `tx_begin`; `openai_chat`: `chat`).
`outcome` is `ok` or `error`.

`darek.dep.*` is **additive** alongside existing LLM-specific metrics. `darek.llm.latency` (labels: `model`, `outcome`) is kept because `model` is a genuinely useful dimension that doesn't fit the uniform shape; `darek.dep.latency{dep="openai_chat"}` complements it for the cross-dep "is anything slow" view. `darek.tokens.*` and `darek.llm.cost_usd` are kept unchanged — those aren't generic dep concepts. The `openai_chat` call site emits both `llm.latency` and `dep.latency` from the same wrapper; the small duplication is worth the simpler labels.

### 3.2 In-process work (kept, with one addition)

- `darek.tool.calls` — counter — labels: `tool`, `outcome` (existing)
- `darek.tool.latency` — histogram — labels: `tool` (existing)
- `darek.turn.duration` — histogram — labels: `outcome` (existing)
- `darek.turn.iterations` — histogram (existing)
- `darek.agent.max_iters_hit` — counter — **new**, no labels. Bumped in the existing max-iters branch in `agent/agent.go`.

### 3.3 Business activity

Counters that answer "what did I do today":

- `darek.mail.envelopes_synced` — counter — labels: `account`
- `darek.mail.bodies_fetched` — counter — labels: `account`
- `darek.mail.attachments_fetched` — counter — labels: `account`
- `darek.mail.sent` — counter — labels: `account`
- `darek.memory.notes_saved` — counter
- `darek.memory.notes_recalled` — counter
- `darek.links.events` — counter — labels: `op` ∈ `{save_new, save_update, search, similar}`

Most tool-level activity is already derivable from `darek.tool.calls`. These counters cover the things that aren't tool calls (`darek mail sync` is a CLI subcommand) and the sub-operations of compound tools (a `links.save` is "new" or "update" — invisible from the tool counter alone).

### 3.4 Runtime & DB

- Go runtime instrumentation via `go.opentelemetry.io/contrib/instrumentation/runtime` — goroutines, GC pauses, heap. One call in `obs.Init`.
- Postgres pool gauges read async from `pgxpool.Pool.Stat()`:
  - `darek.db.pool.acquired` (gauge)
  - `darek.db.pool.idle` (gauge)
  - `darek.db.pool.total` (gauge)

### 3.5 Cardinality

Active-series ceiling estimate: ~500. Limits:

- `dep` ≤ 10
- `op` per-dep ≤ ~5
- `tool` ~ 15
- `account` 1–3
- `model` 1–2
- `outcome` 2
- `links` op = 4

**Rule (enforced by review and a unit test):** no free-form fields (URLs, IDs, user input, error strings) in metric labels. Only fixed enums.

### 3.6 Process-model caveat

Darek today is a one-shot CLI. Counters and histograms are short-lived; OTLP flush on shutdown (already done in `obs.Init`'s shutdown fn) ensures the last batch reaches the collector. Runtime metrics are a per-invocation snapshot, not a trend — the runtime dashboard is labeled accordingly.

## 4. Dashboards

All seven JSON files live in `otel/grafana/dashboards/`, provisioned via the existing `dashboards.yml`.

### 4.1 `darek — overview` (NEW)

Top row, 5 stat panels (24h):

- Turns today
- Tokens today
- USD today
- Error rate (last 1h)
- Active goroutines (now)

Activity row:

- Tool calls/min stacked by `tool` (top 10)
- Mail synced today · Links saved today · Notes saved today (stat trio)

Cost & latency row:

- $/hour by model
- Turn p50/p95
- LLM p95 by model
- Dep p95 by `dep` (stacked timeseries)

Health row:

- Errors/min by component (`agent`, `llm`, `tool`, `dep`) — stacked area
- Top 5 failing ops table (`dep` + `op` + error rate %, sorted desc)

Each panel links to the relevant detail dashboard.

### 4.2 Refreshed (existing)

- `darek — agent turns` — adds: `agent.max_iters_hit` rate, error rate %, turn outcome breakdown.
- `darek — tokens & cost` — adds: tokens by kind (input/output/cached) stacked, cache hit % stat.
- `darek — tools` (renamed from "tool latency") — adds: error rate by tool, success vs error stacked, p50/p95/p99 trio.

### 4.3 New drill-downs

- `darek — external deps` — per-dep RPS · per-dep p50/p95 · error rate by `dep`+`op` · top slow ops table.
- `darek — mail` — envelopes synced/min by account · bodies/attachments fetched/min · sends today · IMAP error rate · sync duration p95.
- `darek — links & memory` — links events by `op` · search/similar latency p95 · saves over time · notes saved/recalled rate.
- `darek — runtime` — goroutines · GC pause p95 · heap in-use · pgx pool gauges · DB query p95 by `op`.

## 5. Implementation shape

### 5.1 New `obs` API

- `obs/dep.go` — `func Dep(ctx, dep, op string, fn func(context.Context) error) error` — wraps a call with span + records `darek.dep.requests` + `darek.dep.latency`. Single helper used at every external call site so labels stay uniform.
- `obs/runtime.go` — `StartRuntime()` called from `obs.Init`.
- `obs/db.go` — registers async gauges that observe `pgxpool.Pool.Stat()`.
- `obs/metrics.go` — adds new instruments (dep counter/histogram, agent.max_iters_hit, mail/memory/links business counters). `LLMLatency` is kept (see §3.1).

### 5.2 Wiring (call sites touched)

- `llm/client.go` — wrap `Chat` body with `obs.Dep("openai_chat", "chat", ...)`.
- `tools/calendar/google.go`, `tools/calendar/ical.go` — wrap HTTP calls.
- `tools/todoist/client.go` — wrap each REST method.
- `tools/freshrss/client.go` — wrap each GReader call.
- `tools/mail/imap/{imap.go,append.go}` — replace bare spans with `Dep` wrapper; bump business counters at the right spots.
- `tools/mail/smtp/smtp.go` — wrap, bump `mail.sent`.
- `db/pool.go` — register pool gauges after pool creation. Add a `db.Querier` wrapper used by all stores; the wrapper records `dep=postgres` metrics. (Choosing `Querier` wrapper over per-call-site instrumentation — one place, no drift.)
- `memory/store.go` — bump `memory.notes_{saved,recalled}` in the right methods.
- `links/store.go` — `Save` decides save_new vs save_update (already known there) and bumps `links.events{op=...}`; `Search`/`Similar` bump too.
- `agent/agent.go` — bump `agent.max_iters_hit` in the existing max-iters branch.

### 5.3 Dashboards

Seven JSON files in `otel/grafana/dashboards/`. Existing three edited in place, four new files added. No grafana-UI editing — edits go through the JSON.

## 6. Testing

- Unit tests for `obs.Dep`: records correct labels on success/error, propagates ctx, calls `fn` exactly once.
- Unit tests for the `db.Querier` wrapper: records on Exec/Query, error path, tx_begin path.
- `obs/metrics_test.go` extended for the new instruments.
- A **cardinality test** that walks the registered metrics and asserts no label has an unbounded value source (specifically: no metric records a label whose values come from request data).
- Manual verification (documented in the plan): `make obs-up`, run a chat turn, run `darek mail sync`, run `links.save` / `links.search` from chat, confirm each dashboard panel populates within ~30s.

## 7. Migration & risk

- All changes are additive — `darek.llm.latency` is kept, no existing dashboard queries break.
- The `openai_chat` site emits both `llm.latency` and `dep.latency`. Cost is bounded (one extra histogram observation per chat call, well below the rate of LLM calls themselves).
- Process-model caveat: short-lived CLI flush is already handled by `obs.Init`'s shutdown fn; no new risk.
