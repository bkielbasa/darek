# Darek — cross-digest memory (design)

**Date:** 2026-10-11
**Status:** draft (awaiting review)
**Author:** brainstormed with opencode

## 0. Position in the larger plan

Darek already has two memory-adjacent systems:

- **Free-form notes** — `notes` table (`db/migrations/0001_initial.up.sql`) plus the `memory.save` / `memory.recall` agent tools (`memory/store.go`, `memory/tools.go`). These only capture what the user explicitly tells the chat agent.
- **The daily digest** — `darek calendar daily-digest` (`cmd/darek/daily_digest.go`) sends a calendar section plus a per-group WhatsApp summary section (`docs/specs/2026-05-05-whatsapp-summary-in-daily-digest-design.md`).

What is missing is *continuity across digests*: Darek reads the WhatsApp chat every morning and throws that understanding away. If a group agrees to move dinner to Sunday, or someone mentions a fact about a friend, the next digest has no recollection of it.

This spec adds **structured, cross-digest memory**: an LLM extraction pass that turns recent WhatsApp conversation into durable records — agreements, events, and facts about people — and a recall pass that surfaces the still-relevant ones as a "Follow-ups" / "People" section in the daily digest. It is the recall counterpart to the write-side calendar tools (`docs/specs/2026-05-01-calendar-write-tools-design.md`) without ever touching the calendar automatically.

## 1. Goal

After `darek calendar daily-digest` runs:

1. New durable memories have been extracted from any WhatsApp messages not yet mined for memories (agreements, events, facts about people).
2. The email gains a **Follow-ups** section listing active agreements/events that are due soon, overdue, or recently mentioned, and a **People** section listing recently-mentioned facts about people.
3. Nothing is changed automatically — the sections are information for the user to act on.

Memories persist across runs and are independently recallable from `darek chat`.

## 2. Scope

### In

- New `memories` table (structured) with kind, people, due date, status, provenance, dedupe key, and a tsvector search column.
- New `whatsapp_messages.memories_extracted_at` column + partial index, mirroring the existing `summarized_at` cursor. Memory extraction gets its **own** cursor so a summary failure and a memory failure cannot suppress one another.
- New `memory.Candidate`, `memory.Memory`, and an LLM-backed `memory.Extractor` (same `Chat`-interface pattern as `whatsapp.Summarizer`) that maps a group transcript to structured candidates as JSON.
- New `memory.Store` methods: `SaveMemories` (dedupe upsert), `ActiveItems`, `PersonFacts`, `SetMemoryStatus`, `ListMemories`.
- New `memory.BuildMemories` orchestrator: per opted-in group, fetch unextracted messages (7-day cap) → extract → persist → mark extracted.
- New `whatsapp.Store` methods: `UnextractedMessages`, `MarkMemoriesExtracted` (the memory cursor; the whatsapp package owns `whatsapp_messages`).
- New `memory.RenderText` / `memory.RenderHTML` for the Follow-ups + People sections.
- `cmd/darek/daily_digest.go` wiring: run `BuildMemories`, then recall + render, appending the sections to the email.
- New `memory.list` agent tool so the chat can recall structured memories.
- `memory.enabled` config flag (default false) gating the whole feature, since extraction costs LLM calls.
- Observability counters for extraction + persistence.

### Out (deferred — separate plans or YAGNI)

- **Automatic calendar changes.** The user explicitly chose "surface only". Follow-ups never call `calendar.update_event`; the user acts.
- **Mail as an extraction source.** Only WhatsApp messages are mined in v1.
- **Calendar events as an extraction source or cross-link.** No `calendar_uid` on memories in v1.
- **Semantic / embedding recall.** `memory.pgvector` stays unused; recall is tsvector + structured filters.
- **Consolidation of near-duplicates** beyond an exact `dedupe_key` match.
- **Chat-side editing of memory status** (accept / dismiss / mark done) — the store has `SetMemoryStatus`, but no agent tool or UI exposes it in v1.
- **Per-group extraction toggles** separate from the existing ingest opt-in.
- **Late reply / follow-through detection** ("did you do the thing you agreed to?").
- **Recurring/relative dates** ("every Tuesday"). Only concrete dates or undated items.

## 3. Architecture

The cron pod (`darek calendar daily-digest`) does **not** connect to WhatsApp; it only reads Postgres. Both the WhatsApp summary pipeline and the new memory pipeline read the same `whatsapp_messages` table but track independent cursors:

```
                         whatsapp_messages
                         ├── summarized_at          ← summary cursor (existing)
                         └── memories_extracted_at  ← memory cursor (new)
                                    │
   runDailyDigest()                 │
     ├── calendar pipeline ────────►► calText / calHTML
     ├── whatsapp pipeline ────────►► waText / waHTML   (uses summarized_at)
     └── memory pipeline ──────────►► memText / memHTML (uses memories_extracted_at)
              │
              ├── BuildMemories()   extract → SaveMemories → MarkMemoriesExtracted
              └── recall()          ActiveItems + PersonFacts → render
                                    │
                    composed email = calendar + follow-ups + people + whatsapp
                                    │
                             send via SMTP
                                    │
                    on success: whatsapp.MarkSummarized(ids)
```

Pieces:

- **`memory/memories.go`** — `Memory`, `Candidate`, `Store` methods for the `memories` table.
- **`memory/extract.go`** — `Chat` interface, `Extractor`, JSON prompting/parsing, `dedupeKey` normalization.
- **`memory/orchestrate.go`** — `TranscriptSource` interface, `ExtractorInterface`, `BuildMemories`, `Stats`.
- **`memory/render.go`** — `FollowupView`, `RenderText`, `RenderHTML`.
- **`memory/recall_tool.go`** — `memory.list` agent tool.
- **`tools/whatsapp/store.go`** (modify) — `UnextractedMessages`, `MarkMemoriesExtracted`.
- **`cmd/darek/daily_digest.go`** (modify) — wire the pipeline in.
- **`cmd/darek/chat.go`** (modify) — register `memory.list`.
- **`db/migrations/0015_memory.up.sql`** — schema.
- **`config/types.go`** (modify) — add `Memory.Enabled`.
- **`obs/metrics.go`** (modify) — new counters.

Layering note: `memory` imports `darek/tools/whatsapp` for the `Message`/`Group` types (whatsapp does not import memory, so no cycle). The `transcript` is a `[]whatsapp.Message`; the orchestrator depends on a narrow `TranscriptSource` interface so tests inject a stub and the real `*whatsapp.Store` satisfies it without changes.

## 4. Schema

`db/migrations/0015_memory.up.sql`:

```sql
CREATE TABLE memories (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    kind         text NOT NULL,                     -- agreement | event | person_fact | other
    summary      text NOT NULL,
    details      text NOT NULL DEFAULT '',
    people       text[] NOT NULL DEFAULT '{}',
    groups       text[] NOT NULL DEFAULT '{}',      -- provenance: whatsapp group JIDs
    due_at       timestamptz,
    status       text NOT NULL DEFAULT 'active',    -- active | done | dismissed | superseded
    dedupe_key   text NOT NULL,
    source       text NOT NULL DEFAULT 'digest',
    source_refs  text[] NOT NULL DEFAULT '{}',      -- provenance: whatsapp message IDs
    search       tsvector GENERATED ALWAYS AS (
        to_tsvector('simple'::regconfig,
            summary || ' ' || details || ' ' ||
            immutable_array_to_string(people, ' ') || ' ' ||
            immutable_array_to_string(groups, ' '))
    ) STORED
);

CREATE UNIQUE INDEX memories_dedupe_key_uniq ON memories (dedupe_key);
CREATE INDEX memories_search_gin  ON memories USING gin(search);
CREATE INDEX memories_people_gin  ON memories USING gin(people);
CREATE INDEX memories_active_due  ON memories (due_at) WHERE status = 'active';
CREATE INDEX memories_kind_status ON memories (kind, status, last_seen_at DESC);

ALTER TABLE whatsapp_messages
    ADD COLUMN memories_extracted_at timestamptz;

CREATE INDEX idx_whatsapp_messages_unextracted
    ON whatsapp_messages (group_jid, sent_at)
    WHERE memories_extracted_at IS NULL;
```

`immutable_array_to_string` is created in migration `0001`. No `down` migration; matches the project's convention.

Design notes:

- **One table, not a `people` table.** The user chose plain structured memory. A person is a set of `kind = 'person_fact'` rows tagged with `people`. This keeps ingestion and recall uniform; a dedicated people table can come later if we outgrow it.
- **`dedupe_key` is the identity of a memory.** The extractor emits a short, stable handle (e.g. `dinner-marek-sunday`). It is normalized server-side (lowercased, whitespace collapsed to `-`). The unique index makes re-extraction idempotent, which is what lets a failed run safely retry.
- **`status` is preserved across upserts.** Seeing a plan mentioned again updates `last_seen_at` and refreshes the text, but never flips a `done`/`dismissed` memory back to `active`.
- **`last_seen_at` vs `updated_at`.** `updated_at` moves on any write; `last_seen_at` moves whenever the extractor re-emits the memory, even if the content is unchanged. Recall uses `last_seen_at` for "recently mentioned".

## 5. Store API

New methods on `memory.Store` (`memory/memories.go`). `Memory` is the row view model:

```go
type Memory struct {
    ID         uuid.UUID
    CreatedAt  time.Time
    UpdatedAt  time.Time
    LastSeenAt time.Time
    Kind       string
    Summary    string
    Details    string
    People     []string
    Groups     []string
    DueAt      *time.Time
    Status     string
    DedupeKey  string
    Source     string
    SourceRefs []string
}
```

```go
// SaveMemories upserts candidates keyed by dedupe_key and returns the number of
// rows written. `group` is the display name of the originating WhatsApp group
// (stored as provenance). On conflict it refreshes summary/details/last_seen_at,
// unions groups and source_refs, COALESCEs due_at, and preserves status.
func (s *Store) SaveMemories(ctx context.Context, cands []Candidate, group string, sourceRefs []string) (int, error)

// ActiveItems returns status='active' agreements/events that are due before
// now+horizon, or undated but seen within `recent` days.
func (s *Store) ActiveItems(ctx context.Context, horizonDays, recentDays, limit int) ([]Memory, error)

// PersonFacts returns status='active' person_fact rows seen within `sinceDays`.
func (s *Store) PersonFacts(ctx context.Context, sinceDays, limit int) ([]Memory, error)

// ListMemories is the chat-recall query: optional full-text query, optional
// kind and status filters, newest-seen first.
func (s *Store) ListMemories(ctx context.Context, query, kind, status string, limit int) ([]Memory, error)

// SetMemoryStatus flips one memory's status. Not exposed to any caller in v1;
// included so recall tooling and a future UI have a single writer.
func (s *Store) SetMemoryStatus(ctx context.Context, id uuid.UUID, status string) error
```

`SaveMemories` SQL (one statement, batched via `pgx.Batch` or a loop inside a transaction):

```sql
INSERT INTO memories
    (kind, summary, details, people, groups, due_at, dedupe_key, source, source_refs)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'digest', $8)
ON CONFLICT (dedupe_key) DO UPDATE SET
    updated_at  = now(),
    last_seen_at = now(),
    summary     = EXCLUDED.summary,
    details     = EXCLUDED.details,
    people      = EXCLUDED.people,
    groups      = (SELECT array(SELECT DISTINCT unnest(memories.groups || EXCLUDED.groups))),
    source_refs = (SELECT array(SELECT DISTINCT unnest(memories.source_refs || EXCLUDED.source_refs))),
    due_at      = COALESCE(EXCLUDED.due_at, memories.due_at)
```

`ActiveItems`:

```sql
SELECT id, created_at, updated_at, last_seen_at, kind, summary, details,
       people, groups, due_at, status, dedupe_key, source, source_refs
  FROM memories
 WHERE status = 'active'
   AND kind IN ('agreement', 'event')
   AND ((due_at IS NOT NULL AND due_at < now() + make_interval(days => $1))
        OR (due_at IS NULL AND last_seen_at >= now() - make_interval(days => $2)))
 ORDER BY due_at ASC NULLS LAST, last_seen_at DESC
 LIMIT $3
```

`PersonFacts`:

```sql
SELECT ... FROM memories
 WHERE status = 'active'
   AND kind = 'person_fact'
   AND last_seen_at >= now() - make_interval(days => $1)
 ORDER BY last_seen_at DESC
 LIMIT $2
```

`ListMemories` builds its WHERE clause conditionally (`search @@ plainto_tsquery('simple', $q)` when a query is given; `kind = $k` / `status = $s` when set) and orders by `last_seen_at DESC`.

## 6. WhatsApp cursor

New methods on `tools/whatsapp.Store`:

```go
// UnextractedMessages returns messages where memories_extracted_at IS NULL and
// sent_at >= now() - lookbackDays, ascending by sent_at.
func (s *Store) UnextractedMessages(ctx context.Context, groupJID string, lookbackDays int) ([]Message, error)

// MarkMemoriesExtracted sets memories_extracted_at = now() for the given IDs.
// Idempotent.
func (s *Store) MarkMemoriesExtracted(ctx context.Context, ids []string) error
```

Identical shape to `UnsummarizedMessages` / `MarkSummarized`, against the new column and partial index. The two cursors are deliberately independent: a group whose summary failed (so `summarized_at` is still null) is unaffected by memory extraction, and vice versa.

## 7. Extractor

```go
// Chat is the subset of *llm.Client used by Extractor. Same pattern as
// whatsapp.Summarizer so tests inject a fake.
type Chat interface {
    Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

type Extractor struct{ llm Chat }

func NewExtractor(c Chat) *Extractor
```

`type Candidate struct { Kind, Summary, Details string; People []string; DueAt *time.Time; DedupeKey string }`

```go
func (e *Extractor) Extract(ctx context.Context, groupName string, msgs []whatsapp.Message) ([]Candidate, error)
```

System prompt:

> You extract durable memories from a WhatsApp group conversation. Return ONLY a JSON object of the form {"memories":[...]}. Each memory: "kind" is one of "agreement" (something the participants agreed or committed to), "event" (a dated plan/occurrence), "person_fact" (a durable fact about a person), or "other". "summary" is one plain sentence in the language of the conversation. "details" is optional supporting context. "people" is the names of people the memory is about. "due_at" is an ISO-8601 datetime (with offset) for a concrete time, or "YYYY-MM-DD" for a date-only plan, or null. "dedupe_key" is a short stable slug identifying the plan/fact (e.g. "dinner-marek-sunday"); reuse the same key when the conversation mentions the same thing again. Only record things that are explicitly stated — never infer, speculate, or record pleasantries. If there is nothing durable, return {"memories":[]}.

User message mirrors the summarizer: `Group: <name>`, then `[YYYY-MM-DD HH:MM] Sender: body` lines, truncated to the most recent `maxTranscriptChars` (6000). To let the model resolve relative dates, the prompt prepends `Today is <YYYY-MM-DD> (<weekday>), timezone <local>.` using the message batch's last timestamp and `time.Local`.

Request params set `ResponseFormat` to JSON object (the `*llm.Client` passes params through, overriding only `Model`). Parsing is tolerant:

- Strip ```/```json code fences if present.
- `json.Unmarshal` into `struct{ Memories []Candidate }`.
- Empty/missing `memories` → `nil, nil` (not an error).
- An invalid kind is coerced to `"other"`.
- Empty `summary` candidates are dropped.
- Missing `dedupe_key` → derive `kind + "-" + slug(summary)`.
- Empty model content → error (`"empty model response"`), so the orchestrator logs and skips the group.

Date parsing: RFC3339 first; then `2006-01-02` as local midnight. Unparseable `due_at` → `nil` (a memory without a date is still useful) rather than failing the group.

## 8. Orchestrator

```go
const memoriesLookbackDays = 7 // mirrors whatsapp.LookbackDays; bounded retry window

type TranscriptSource interface {
    OptedInGroups(ctx context.Context) ([]whatsapp.Group, error)
    UnextractedMessages(ctx context.Context, groupJID string, lookbackDays int) ([]whatsapp.Message, error)
    MarkMemoriesExtracted(ctx context.Context, ids []string) error
}

type ExtractorInterface interface {
    Extract(ctx context.Context, groupName string, msgs []whatsapp.Message) ([]Candidate, error)
}

type Stats struct {
    Groups      int
    Extracted   int // candidates produced
    Saved       int // rows written
    FailedGroups int
}

func BuildMemories(
    ctx context.Context,
    src TranscriptSource,
    store *Store,
    extractor ExtractorInterface,
    logger func(format string, a ...any),
) (Stats, error)
```

Flow:

1. `groups, err := src.OptedInGroups(ctx)`; on error return `(Stats{}, err)`.
2. For each group:
   1. `msgs, err := src.UnextractedMessages(ctx, g.JID, memoriesLookbackDays)`. On error, log + skip group (do **not** abort the run; a single bad group shouldn't block the others).
   2. If empty, skip.
   3. `cands, err := extractor.Extract(ctx, g.Name, msgs)`. On error, log + skip group; its IDs are **not** marked, so the next run retries.
   4. If `len(cands) == 0`, still mark the IDs extracted (the group was read; there is nothing durable).
   5. `n, err := store.SaveMemories(ctx, cands, g.Name, msgIDs(msgs))`. On error, log + skip group without marking (retry next run; `dedupe_key` makes the retry idempotent).
   6. `src.MarkMemoriesExtracted(ctx, msgIDs(msgs))`. On error, log; not fatal — the next run re-extracts, `dedupe_key` dedupes.
3. Return accumulated `Stats`.

`logger == nil` becomes a no-op. `BuildMemories` returns a non-nil error only for the initial `OptedInGroups` failure; all per-group failures are logged and reflected in `Stats.FailedGroups`.

## 9. Recall + rendering

Recall happens in the digest process (not `BuildMemories`):

```go
const (
    followupHorizonDays     = 14
    followupRecentDays      = 7
    followupLimit           = 10
    personFactLookbackDays  = 3
    personFactLimit         = 8
)

type FollowupView struct {
    Items  []Memory // agreements + events
    People []Memory // person facts
}

func Recall(ctx context.Context, store *Store) (FollowupView, error)
func RenderText(v FollowupView) string
func RenderHTML(v FollowupView) string
```

`Recall` calls `ActiveItems(followupHorizonDays, followupRecentDays, followupLimit)` and `PersonFacts(personFactLookbackDays, personFactLimit)`. A failure to fetch people facts does not fail the fetch of items (each error independently logged by the caller); `Recall` returns the partial view plus the first error.

Empty view → both renderers return `""`.

### Text format

```
Follow-ups
──────────
▸ Sun 18 Oct 19:00 — Dinner with Marek (Family)
▸ overdue since Fri 10 Oct — Send the deploy plan (Work)
▸ Anna's birthday is in June (Friends)

People
──────
• Anna — son Kuba plays football
• Marek — allergic to shellfish
```

Undated items show `(recent)` instead of a date. The group is shown when known (first `groups` entry resolved to a name is out of scope for v1 — v1 stores the JID; see risks). Items are one line each, wrapped at 78 columns.

> Open point for review: v1 stores the group **JID** in `memories.groups`, which renders as an opaque string. Two options: (a) render no group in the Follow-ups line, or (b) have `BuildMemories` store the human group **name** instead of/alongside the JID. This spec assumes **(b)**: `memory.BuildMemories` passes `g.Name` so `groups` holds display names (provenance by name is enough for a personal system; the JID adds nothing to the email). Feedback welcome.

### HTML format

Same card vocabulary as the calendar and WhatsApp sections: `<section>` with an `<h2>`, one bordered card per item, dimmed meta. Inline CSS only. User-supplied strings HTML-escaped with the same `htmlEscape` helper the WhatsApp renderer uses.

Section order in the email: calendar → **Follow-ups / People** → WhatsApp. Follow-ups are forward-looking and sit next to the calendar; WhatsApp is "what happened".

## 10. Daily digest integration

In `cmd/darek/daily_digest.go`, after the existing WhatsApp summary block and before `subject := d.Subject`:

```go
var memText, memHTML string
if cfg.Memory.Enabled && cfg.WhatsApp.Enabled && pool != nil {
    if apiKey, _ := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv); apiKey != "" {
        llmClient, err := llm.New(llm.Options{
            APIKey: apiKey, BaseURL: cfg.OpenAI.BaseURL,
            Model: cfg.OpenAI.Model, Timeout: cfg.Agent.LLMTimeout,
        })
        if err != nil {
            fmt.Fprintf(os.Stderr, "warn: llm client for memory digest: %v\n", err)
        } else {
            waStore := whatsapp.NewStore(pool)
            memStore := memory.NewStore(pool)
            stats, err := memory.BuildMemories(ctx, waStore, memStore,
                memory.NewExtractor(llmClient), logf)
            if err != nil {
                fmt.Fprintf(os.Stderr, "warn: memory extraction failed: %v\n", err)
            }
            view, rerr := memory.Recall(ctx, memStore)
            if rerr != nil {
                fmt.Fprintf(os.Stderr, "warn: memory recall failed: %v\n", rerr)
            }
            memText = memory.RenderText(view)
            memHTML = memory.RenderHTML(view)
            fmt.Fprintf(os.Stderr, "info: memory: groups=%d extracted=%d saved=%d failed=%d\n",
                stats.Groups, stats.Extracted, stats.Saved, stats.FailedGroups)
        }
    }
}
```

Composition changes from `text := calText` + append WhatsApp to:

```go
text := calText
html := calHTML
if memText != "" { text += "\n\n" + memText; html += memHTML }
if waText  != "" { text += "\n\n" + waText;  html += waHTML }
```

Extraction runs **before** the email is sent, but its persistence is independent of email success: unlike `MarkSummarized`, `MarkMemoriesExtracted` is called inside `BuildMemories` and does not wait for SMTP. If the email fails to send, the memories are still recorded (they are internal state, not a notification). This is intentional.

The whole memory block is skipped when any of: `memory.enabled` is false, WhatsApp is disabled, the Postgres pool is absent, or OpenAI is unconfigured. The calendar + WhatsApp digest still goes out.

## 11. Chat recall tool

New `memory.list` tool in `memory/recall_tool.go`, registered in `cmd/darek/chat.go` next to the existing `memory.recall` / `memory.save`:

```go
type ListTool struct{ Store *Store }

func (ListTool) Name() string        { return "memory.list" }
func (ListTool) Description() string { return "Recall durable memories (agreements, events, facts about people) learned from earlier digests and conversations." }
```

Schema: `query` (string, full-text over summary/details/people), `kind` (enum agreement|event|person_fact|other, optional), `status` (enum active|done|dismissed|superseded, default active), `limit` (1–20, default 8). Output is one line per memory: date/status, kind, summary, people. `memory.recall` (free-form notes) keeps its current behavior; the two are complementary and both remain registered.

## 12. Configuration

Add one field to `config.Memory`:

```go
type Memory struct {
    Pgvector       bool   `yaml:"pgvector"`
    EmbeddingModel string `yaml:"embedding_model"`
    Enabled        bool   `yaml:"enabled"` // new: enables digest extraction + recall
}
```

No other config. The feature is gated by `memory.enabled: true` (opt-in, because extraction costs LLM calls) plus the already-required `whatsapp.enabled` and `openai.api_key_env`. Helm values set `memory.enabled: true` when the image is deployed.

The numeric constants in §9 (horizon, lookback, limits) are named Go constants, not config, matching how `whatsapp.LookbackDays` is handled.

## 13. Observability

Two new counters in `obs/metrics.go`, following the existing `darek.memory.*` naming:

- `darek.memory.memories_extracted` — candidates returned by the extractor.
- `darek.memory.memories_saved` — rows upserted into `memories`.

The extractor's `*llm.Client.Chat` calls already flow into `darek.tokens.*`, `darek.llm.cost_usd`, `darek.llm.latency`, `darek.dep.*`. The existing `darek.memory.notes_saved` / `notes_recalled` stay for the free-form notes path. `BuildMemories` returns `Stats`; the digest logs it once per run for a cheap at-a-glance signal.

## 14. Testing

Unit (`memory/extract_test.go`, `memory/render_test.go` — no build tag):

- `Extractor` happy path: fake `Chat` returns a JSON object; assert the captured user message contains `Group: …`, the `Today is …` header, and time-prefixed lines; returned candidates match.
- `Extractor` tolerates fenced JSON (```json … ```).
- `Extractor` invalid JSON → error.
- `Extractor` empty model content → error.
- `Extractor` drops empty-summary candidates, coerces unknown kinds to `other`, derives a dedupe key when missing, parses both RFC3339 and date-only `due_at`, and nulls unparseable dates.
- `Extractor` transcript truncation (tail-bias) like `TestSummarize_TruncatesLongTranscriptToTail`.
- `slug`/`dedupeKey` normalization table test.
- `RenderText` / `RenderHTML` golden tests: dated item, undated "recent" item, overdue item, People list, empty view → `""`, hostile group/person names escaped.

Integration (`memory/memories_integration_test.go`, build tag `integration`, pg testcontainer):

- `SaveMemories` inserts; re-running with the same `dedupe_key` updates `last_seen_at`, unions `groups`/`source_refs`, does not duplicate.
- `SaveMemories` preserves a non-active `status` across an upsert.
- `SaveMemories` `COALESCE`s `due_at` (an incoming null does not erase a stored date).
- `ActiveItems` includes due-soon and overdue active agreements/events, excludes `done`/`dismissed` and out-of-window undated items, and excludes `person_fact`.
- `PersonFacts` filters by kind and `last_seen_at` window.
- `ListMemories` query/kind/status filters.
- `whatsapp.Store.UnextractedMessages` returns only `memories_extracted_at IS NULL` within the lookback.
- `whatsapp.Store.MarkMemoriesExtracted` flips only the listed IDs and is idempotent; it does **not** touch `summarized_at`.
- `BuildMemories` end-to-end with a stub `TranscriptSource` + stub `ExtractorInterface`: two groups (one with messages, one without) → candidates saved once, messages marked; a failing extractor for one group does not abort the other and leaves that group's IDs unmarked; a group with zero candidates still gets marked.

Build/manual:

- `go build ./...`, `make test`, `make lint`.
- Manual smoke (post-deploy): apply migration, trigger the cron, verify the email gains Follow-ups/People, then trigger again and confirm extraction is a no-op (all messages `memories_extracted_at` set). Run twice with an induced extractor failure (e.g. bad API key) to confirm retry marking.

## 15. Risks / edge cases

- **Cost.** One extra LLM call per group with new messages per day, bounded by the 6000-char transcript cap. Roughly doubles the per-day WhatsApp LLM cost; still well under a cent per group on the cheap model.
- **Hallucination.** Durable memories are higher-stakes than a summary ("you agreed to X" can be wrong). Mitigations: a strict "only explicitly stated, never infer" system prompt; JSON schema; the user sees every memory in the digest and can correct course; nothing acts automatically. Accepted for v1.
- **Dedupe drift.** The model may emit a different `dedupe_key` for the same plan (phrasing drift), producing duplicates. Mitigation: idempotency on exact matches; duplicates are visible and harmless in a personal list. A consolidation pass is deferred.
- **JSON mode portability.** Some OpenAI-compatible `base_url` backends lack `response_format: json_object`. Mitigation: tolerant parsing (code-fence stripping) and a clear error → group skipped and retried; the rest of the digest is unaffected.
- **Timezone / relative dates.** "Sunday" resolution relies on the prompt's `Today is … (weekday)` header and the batch's last timestamp. Date-only plans are stored as local midnight. A wrong guess yields a wrong `due_at` but never a wrong action (nothing auto-applies).
- **Retry duplication.** Marking happens only after a successful persist; a crash between persist and mark re-extracts next run, and `dedupe_key` prevents duplicate rows.
- **Group JID vs name (see §9).** v1 stores display names for readability; if a group is renamed, older memories keep the old name. Acceptable.
- **Messages with media.** `kind != text` messages have placeholder bodies, so memories are extracted only from text content. Expected.
- **Empty day.** No new messages and no active follow-ups → the section is omitted and the email is unchanged.
- **Names on WhatsApp.** `sender_name` is a push name and may be an alias; `people` may not match how the user thinks of them. Accepted.

## 16. Out of scope (recap)

- Automatic calendar updates (surface only).
- Mail / calendar as extraction sources; calendar cross-links.
- Embedding/pgvector semantic recall.
- Consolidation of near-duplicates.
- Chat-side status editing (accept/dismiss/done).
- Per-group extraction toggles; late-reply detection; recurring dates.
</content>
