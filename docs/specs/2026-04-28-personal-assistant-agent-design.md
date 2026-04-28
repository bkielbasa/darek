# Darek — personal assistant agent (design)

**Date:** 2026-04-28
**Status:** approved (awaiting implementation plan)
**Author:** brainstormed with Claude

## 1. Goal

Build a Go-based personal-assistant CLI that uses the OpenAI API as its model and integrates with Todoist, calendars (Google + iCal feeds), and mail (IMAP/SMTP). The agent supports read-only Q&A, mutating Todoist actions, multi-step planning, and cross-session memory. First-class observability — traces, metrics (especially token usage and cost), and structured logs — is a primary requirement, not a nice-to-have.

The system ships first as a CLI on the user's laptop. The architecture is shaped so that a future hosted Kubernetes service is a deployment change, not a rewrite.

## 2. MVP scope

### In

- **Read-only Q&A** across Todoist, calendars, and mail.
- **Mutating Todoist actions:** create, complete, update, reschedule tasks.
- **Multi-step planning** — e.g., "look at tomorrow's calendar and create Todoist prep tasks for each meeting."
- **Cross-session memory** — notes recalled in future conversations, backed by Postgres.
- **Mail (IMAP receive + SMTP send), multiple accounts, attachments**, with confirm-before-send and a hybrid sync model (envelopes cached, bodies/attachments fetched on demand).
- **Calendars (Google + iCal feed), read-only**, behind a pluggable `CalendarSource` interface.
- **Local development stack** for observability via docker-compose (OTEL Collector, Jaeger, Prometheus, Grafana with seeded dashboards).

### Out (deferred)

- **ActualBudget** integration. Dropped from MVP after assessing complexity (no native Go client; encrypted CRDT-synced SQLite). May return later if/when the user wants it, likely via a Node sidecar wrapping `@actual-app/api`.
- **Calendar mutations** (read-only for now).
- **Gmail API / Microsoft Graph mail providers.** IMAP/SMTP only for MVP; the `MailAccount` interface allows them as drop-ins later.
- **CalDAV / Outlook calendar sources.** Same — pluggable interface, deferred implementations.
- **Proactive / scheduled jobs** ("morning digest at 8am") — needs service mode.
- **Service / k8s deployment.** Architecture supports it, but MVP is CLI only.
- **Voice / transcription, document upload, MCP server-mode, web UI, multi-user.**

## 3. High-level architecture

```
┌────────────────────────────────────────────────────────────────────┐
│                        cmd/darek (CLI binary)                      │
│  parses args, sets up OTEL, opens DB, builds agent, runs one turn  │
└────────────────────────────┬───────────────────────────────────────┘
                             │
                ┌────────────▼────────────┐
                │      agent              │   tool-calling loop,
                │   (orchestrator)        │   turn limit, OTEL spans
                └─┬───────┬───────────┬───┘
                  │       │           │
       ┌──────────▼──┐ ┌──▼────────┐ ┌▼─────────────┐
       │  llm        │ │ tools     │ │  memory      │
       │ (OpenAI     │ │ (registry │ │ (Postgres-   │
       │  client +   │ │  + impls) │ │  backed      │
       │  retries)   │ │           │ │  notes)      │
       └─────────────┘ └─┬─────────┘ └──────────────┘
                         │
        ┌────────────────┼─────────────────┐
        ▼                ▼                 ▼
   tools/todoist   tools/calendar     tools/mail
                  ├─ google           ├─ imap
                  └─ ical             └─ smtp
```

**Boundary rules:**

- `agent` knows only the `LLM` and `Tool` interfaces. No integration imports.
- `tools/*` is the only place that imports integration SDKs.
- `llm` is the only place that imports the OpenAI Go SDK.
- `cmd/darek` is the only place that wires everything together.
- `obs` is imported everywhere; depends on nothing.

This means swapping the model provider, adding a new calendar source, or adding a future HTTP/k8s entrypoint are each single-package changes.

## 4. Agent loop

In `agent/agent.go`, single goroutine per turn:

```
1. Build messages: [system_prompt, ...recalled_memory, user_input]
2. Call OpenAI Chat Completions with tools=registered_tools
3. If response has tool_calls:
     For each tool_call:
       - look up tool in registry by name
       - validate args via JSON Schema
       - execute (per-tool timeout, OTEL span, retry policy)
       - append tool result message
     Goto 2.
   Else:
     Return final assistant message
4. Hard cap: MAX_ITERATIONS=10. If exceeded, return error + dump full trace.
```

No agent framework. Hand-rolled, ~200 lines. If reliability of multi-step plans becomes a problem later, a planner/executor split is an easy refactor inside `agent/`.

### Tool interface

```go
type Tool interface {
    Name() string
    Description() string
    JSONSchema() json.RawMessage
    Execute(ctx context.Context, args json.RawMessage) (result string, err error)
}
```

`Execute` always returns a string (the content the model sees). Structured data is JSON-encoded into that string.

### Tool registry

`tools.Registry` is a `map[string]Tool` populated in `cmd/darek/main.go`. A tool that fails to construct (missing creds, etc.) logs a warning and is skipped — the agent still runs without it.

### MVP tool catalog

| Tool | Mutating? | Confirm? |
|---|---|---|
| `memory.recall(query)` | no | no |
| `memory.save(note, tags)` | yes (local) | no |
| `todoist.list_tasks(filter)` | no | no |
| `todoist.create_task(content, project, due, ...)` | yes | no |
| `todoist.complete_task(id)` | yes | no |
| `todoist.update_task(id, ...)` | yes | no |
| `calendar.list_events(from, to, calendar?)` | no | no |
| `mail.search(query, account?, folder?, since?, limit?)` | no | no |
| `mail.get_body(account, message_id)` | no | no |
| `mail.get_attachment(account, message_id, attachment_id)` | no | no |
| `mail.send(account, to, subject, body, in_reply_to?, attachments?)` | yes | **yes** |

### Confirmation policy

- **Mail send is the only gated tool in MVP.** Mail is irreversible and externally visible. The tool itself renders a preview and reads y/N from stdin in CLI mode. The interface is abstracted (`Confirmer`) so service mode can later swap in a "pending approval" record without touching the tool. If declined, the tool returns `"user declined to send: <reason>"` and the agent decides what to do next.
- **Todoist mutations are not gated.** Trivial to undo and the failure mode is "too many tasks," not data exfiltration. If the user wants a global mutation gate later, it's a one-line change in the registry.

### System prompt

Short and stable, in `agent/prompt.go` as a string constant. Includes today's date, the auto-listed available tools (with account nicknames), high-level user persona pulled from memory, tone/format rules. Long-tail context (calendar, recent mail) comes via tool calls, not the prompt.

## 5. Mail — hybrid sync model

The user's chosen architecture: cached metadata + headers in Postgres, fetched bodies/attachments on demand from IMAP.

### Sync (`darek mail sync`, or service-mode goroutine)

- Per account, per configured folder: track `UIDVALIDITY` and `last_uid`.
- Pull new envelopes (headers, flags, internal date, threading refs) and a snippet (first ~500 chars of plain body) into `mail_messages`.
- Update flag changes (e.g., `\Seen`) for messages within a recent window.
- Detect deletions by UID gap or `EXPUNGE` notifications, mark rows soft-deleted.
- Write attachment **metadata only** (`mail_attachments_meta`); no bytes downloaded during sync.

### Search

`mail.search` queries Postgres only. Full-text via a generated `tsvector` over `subject + snippet + addrs`. Filters by account nickname, folder, since-date. Returns a compact list (id, from, subject, date, snippet, has_attachments).

### Body / attachment fetch

`mail.get_body` and `mail.get_attachment` fetch live from IMAP using stored `(account_id, folder_id, imap_uid, imap_part_id)`. Attachments are written to `~/.darek/attachments/<message-uuid>/<filename>` (the workspace dir) so follow-up turns can reference them. A nightly GC removes attachment dirs older than `ATTACHMENT_TTL_DAYS=30`.

### Send

`mail.send` builds an RFC 5322 message (with proper `In-Reply-To` / `References` if replying) and dispatches via SMTP. Renders a confirmation preview (To / Subject / body / attachments) and waits for `y/N`. After a successful SMTP send, the message is also appended to the account's Sent folder via IMAP (`APPEND` with `\Seen`), so the sent message is visible in the user's normal mail client. **No retries on send.**

## 6. Persistence (Postgres)

Migrations via `golang-migrate`. Schema kept small:

```sql
notes (
  id uuid pk, created_at timestamptz, updated_at timestamptz,
  body text, tags text[],
  embedding vector(1536) null,    -- pgvector, opt-in
  source text                     -- 'user' | 'agent_save' | 'system'
)
-- Recall is hybrid: tag/text filter first, then optional cosine on embedding.
-- pgvector is opt-in via env; without it, recall is plain tsvector ranking.

turns (
  id uuid pk, started_at timestamptz, ended_at timestamptz,
  user_input text, final_output text,
  trace_id text,                  -- link to OTEL trace
  iterations int,
  input_tokens int, output_tokens int, cost_usd numeric(10,6)
)

messages (
  id uuid pk, turn_id uuid fk, ord int,
  role text,                      -- system|user|assistant|tool
  content text,
  tool_name text null, tool_args jsonb null, tool_result text null
)

mail_accounts (
  id uuid pk, nickname text unique, email text,
  imap_host text, imap_port int, imap_tls bool,
  smtp_host text, smtp_port int, smtp_tls bool,
  username text,
  secret_ref text                 -- pointer; never the secret itself
)

mail_folders (
  id uuid pk, account_id uuid fk, name text,
  uidvalidity bigint, last_uid bigint, last_sync_at timestamptz,
  unique(account_id, name)
)

mail_messages (
  id uuid pk, account_id uuid fk, folder_id uuid fk,
  imap_uid bigint, message_id text, in_reply_to text, references text[],
  thread_key text,
  from_addr text, to_addrs text[], cc_addrs text[],
  subject text, date timestamptz, flags text[],
  snippet text, has_attachments bool,
  search tsvector                 -- generated column; gin index
)

mail_attachments_meta (
  id uuid pk, message_id uuid fk,
  filename text, content_type text, size_bytes bigint,
  imap_part_id text
)

mail_pending_sends (
  id uuid pk, created_at timestamptz, account_id uuid fk,
  to_addrs text[], subject text, body text,
  attachments jsonb,
  status text                     -- pending|sent|cancelled
)
```

Notes:

- `pgvector` is opt-in. Without it, memory recall uses tsvector + tag filter; works fine for hundreds of notes. Embedding-based recall flips on later by setting `memory.pgvector: true`.
- Mail bodies are not cached (per the hybrid choice).
- Attachment files live on disk, never in Postgres.
- `turns` + `messages` paired with the OTEL trace ID is the debugging surface.

## 7. Observability

Headline requirement; gets the most engineering care.

### Instrumentation

OpenTelemetry Go (`go.opentelemetry.io/otel`, `otelhttp`, `otelsql`, `otelslog`). All three signals — traces, metrics, logs — exported via OTLP/gRPC. Backend-agnostic; local dev uses an OTEL Collector.

### Span model (per turn)

Following the [OTEL GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/):

```
darek.turn (root)                                 turn_id, user_input_chars
├── memory.recall                                 db.system, hits
├── chat (gen_ai.operation.name=chat) [iter 1]    gen_ai.request.model,
│                                                 gen_ai.usage.input_tokens,
│                                                 gen_ai.usage.output_tokens
├── tool.execute name=todoist.list_tasks          tool.name, tool.args_hash, tool.result_chars
├── chat (gen_ai.operation.name=chat) [iter 2]    ...
├── tool.execute name=mail.search                 ...
└── memory.persist                                rows_written
```

Every LLM call carries the GenAI standard attributes so any OTLP backend renders token usage natively.

### Metrics

Emitted from a single place (`obs/metrics.go`) so naming stays consistent.

| Metric | Type | Labels |
|---|---|---|
| `darek.tokens.input` | counter | `model`, `tool` (if inside tool subturn) |
| `darek.tokens.output` | counter | `model` |
| `darek.tokens.cached` | counter | `model` |
| `darek.llm.latency` | histogram | `model`, `outcome` |
| `darek.llm.cost_usd` | counter | `model` (derived from token counts × price table) |
| `darek.tool.calls` | counter | `tool`, `outcome` |
| `darek.tool.latency` | histogram | `tool` |
| `darek.turn.duration` | histogram | `outcome` |
| `darek.turn.iterations` | histogram | — |
| `darek.mail.sync.messages` | counter | `account`, `direction` (`new`/`updated`/`deleted`) |
| `darek.mail.sync.duration` | histogram | `account` |

`darek.llm.cost_usd` is derived in-process from a hardcoded per-model price table updated alongside model config changes.

### Logs

Structured JSON via `slog`, OTLP-exported with trace/span IDs auto-injected. **No PII in logs:** no message bodies, no email contents, no Todoist task contents — only IDs, counts, durations. A redactor in `obs/` scrubs known token shapes (Bearer prefixes, JWT-like strings, anything > 32 chars from a "secret"-tagged field) on the way out.

### Local dev

`docker-compose.observability.yml` brings up OTEL Collector + Jaeger + Prometheus + Grafana with pre-seeded dashboards: "agent turns", "tokens & cost", "tool latency", "mail sync".

`OBS_DEBUG=1` dumps spans to stdout in pretty form for when docker-compose isn't running.

## 8. Configuration & secrets

Single config file at `~/.darek/config.yaml`, overridable per env via `DAREK_CONFIG`. Env vars override file values for k8s-friendliness.

```yaml
openai:
  model: gpt-4.1
  base_url: ""

postgres:
  url_env: DAREK_POSTGRES_URL

otel:
  service_name: darek
  exporter_endpoint: localhost:4317
  insecure: true

agent:
  max_iterations: 10
  llm_timeout: 60s
  tool_timeout: 30s

memory:
  pgvector: false
  embedding_model: text-embedding-3-small

todoist:
  token_env: DAREK_TODOIST_TOKEN

calendars:
  - kind: google
    nickname: personal
    creds_env: DAREK_GCAL_PERSONAL
  - kind: ical
    nickname: family
    url: https://calendar.example.com/feed.ics

mail:
  attachments_dir: ~/.darek/attachments
  attachment_ttl_days: 30
  accounts:
    - nickname: personal
      email: me@example.com
      imap: { host: imap.fastmail.com, port: 993, tls: true }
      smtp: { host: smtp.fastmail.com, port: 465, tls: true }
      username: me@example.com
      secret_env: DAREK_MAIL_PERSONAL
      sync_folders: [INBOX, Sent]
```

### Secret rules

1. **No secrets in YAML.** Every credential field is a `*_env` reference. In CLI mode, env loaded from `~/.darek/secrets.env` (gitignored, `chmod 600`). In k8s, mounted Secret as env vars. Same code, no branching.
2. **Optional OS-keyring backend.** `secret_env` can be replaced with `secret_keyring` (macOS Keychain via `zalando/go-keyring`). Resolved at startup; never logged.
3. **Redaction in obs.** All telemetry passes through the redactor (see §7).

### Multi-account addressing

Tools take a `nickname` string instead of an account UUID — the LLM picks. e.g. `mail.search(query="invoice", account="work")`. If omitted, the tool searches all accounts. Nicknames are listed in the system prompt.

### Bootstrap subcommands

- `darek migrate` — runs DB migrations.
- `darek mail sync [--account=<nickname>]` — runs the mail sync once. Cron-friendly.
- `darek calendar refresh-token <nickname>` — interactive Google OAuth.
- `darek doctor` — checks DB connectivity, every configured integration, OTEL exporter reachability; prints a clean status table.

## 9. Error handling

Three layers, three rules:

1. **Tools fail soft, the agent decides.** A tool error returns a result string like `"error: Todoist API rate limited; retry in 60s"` to the model — not a Go error that aborts the turn. Hard errors (DB down, OpenAI auth busted) abort the whole turn with a clear stderr message. The line: anything wrong with the *external integration* is soft; anything wrong with *darek itself* is hard.
2. **Retries live where they're cheap and idempotent.** OpenAI calls retry with jitter on 429/5xx (3 attempts). IMAP fetches retry once. SMTP send does **not** retry. Todoist mutations don't retry.
3. **Every error path is a span event.** No silent failures.

## 10. Testing strategy

- **Unit tests** for: tool argument validation, schema generation, agent loop state machine (LLM stub server returning scripted tool calls), redactor.
- **Integration tests via `testcontainers-go`:** Postgres + a fake IMAP server (`emersion/go-imap` server lib) + a fake SMTP server (`mhale/smtpd`) + a stub OpenAI server (`httptest.Server` returning canned tool-call responses). Real wire protocols, no live network.
- **Contract tests** per integration: small set of tests against the *real* APIs, gated behind `DAREK_E2E=1`. Run nightly or manually. Skip in CI by default.
- **Golden traces.** Some agent tests assert on the *shape of the trace* (span names, attribute keys), not just final answer. This keeps observability from rotting — a refactor that drops a span fails a test.
- **No mocking the LLM client.** A deterministic stub HTTP server lets tests script (assistant_message | tool_calls) sequences.

## 11. Repo layout

```
darek/
  cmd/darek/             main.go, subcommand dispatch
  agent/                 loop, prompt, types
  llm/                   OpenAI client wrapper, retries, cost calc
  memory/                Postgres-backed notes; recall + save
  obs/                   OTEL setup, metrics, redactor, slog wiring
  tools/
    registry.go          Tool interface + registry
    todoist/
    calendar/
      calendar.go        CalendarSource interface
      google/
      ical/
    mail/
      mail.go            MailAccount interface
      imap/              receive + sync
      smtp/              send
      sync.go            sync orchestration
      pending.go         confirmation + pending-send store
  config/                YAML loader, env override, validation
  db/                    pgx pool, migrations (embedded)
  internal/testutil/     fake servers, fixtures
  migrations/            *.sql files
  docker-compose.yml                    postgres
  docker-compose.observability.yml      collector + jaeger + prom + grafana
  grafana/dashboards/                   pre-seeded dashboards
  Makefile               common dev tasks
  README.md              setup, ops, troubleshooting
```

Flat layout — no `pkg/` wrapper directory; tools grouped under top-level `tools/`.

## 12. Risks and known costs

- **Schema drift in upstream APIs.** Todoist, Google Calendar, IMAP are stable; the bigger risk is OpenAI's tool-calling response shape evolving. Pin SDK version; rely on contract tests to catch.
- **Mail sync correctness is fiddly.** UIDVALIDITY changes, expunges, IMAP servers that lie about flags. Mitigation: integration tests against `emersion/go-imap` server fixtures covering each edge case explicitly.
- **Multi-account mail latency.** Live body fetch hits the IMAP server every time. Acceptable for MVP; if it bites, we add a small body LRU cache on disk.
- **Token cost runaway.** A buggy tool that returns megabytes of text into the model can burn dollars fast. Mitigation: tools enforce `MAX_TOOL_RESULT_CHARS=20000` truncation with a warning suffix.
- **Confirmation friction in CLI.** y/N prompt mid-turn breaks pipeline use. Mitigation: an `--auto-approve` flag for power use that the user opts into per-invocation.

## 13. Acceptance criteria

A working MVP means:

1. `darek doctor` reports green for Postgres, OpenAI, OTEL, at least one calendar source, at least one mail account.
2. `darek "what's on my calendar tomorrow?"` returns a correct answer using `calendar.list_events`.
3. `darek "what's overdue in todoist?"` returns a correct answer using `todoist.list_tasks`.
4. `darek "create a Todoist task to call mom"` creates the task and confirms.
5. `darek "draft a reply to Anna's last email"` composes a reply, prompts for y/N, sends via SMTP, and the sent message appears in the user's Sent folder.
6. `darek "remember I'm tracking a Berlin trip"` saves a note; in a fresh invocation, `darek "what trips am I tracking?"` recalls it.
7. `darek "look at tomorrow's calendar and create Todoist prep tasks for each meeting"` performs a multi-step plan: lists events, then creates one task per event.
8. Jaeger shows a clean trace per turn with token usage on each LLM span.
9. Grafana shows non-zero values on the "tokens & cost" and "tool latency" dashboards.
10. All unit + integration tests pass (`make test`); `DAREK_E2E=1 make e2e` passes against real providers.
