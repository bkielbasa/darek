# Darek — WhatsApp summary in the daily digest email (design)

**Date:** 2026-05-05
**Status:** approved (awaiting implementation plan)
**Author:** brainstormed with Claude

## 0. Position in the larger plan

This is the **summarize** half of the originally-planned sub-project B (`whatsapp.summarize_group` agent tool), reframed: instead of an agent tool you invoke from `darek chat`, the summary is appended to the existing daily digest email. Sub-project A (connection + ingest) shipped on 2026-05-04 and 2026-05-05; this spec extends the cron-driven `darek calendar daily-digest` to also include a WhatsApp section.

The chat-side `whatsapp.summarize_group` agent tool, if ever needed, is a follow-up that reuses the `Summarizer` type built here.

## 1. Goal

When `darek calendar daily-digest` runs, the email it sends already covers the next 3 days of calendar events. Add a second section below the calendar with one short LLM-generated summary per opted-in WhatsApp group, covering messages we have not previously summarized. After the email is sent successfully, mark those messages so tomorrow's digest does not repeat them.

## 2. Scope

### In

- New `summarized_at timestamptz` column on `whatsapp_messages` plus a partial index on unsummarized rows.
- New `Summarizer` type in `tools/whatsapp/summary.go` wrapping the existing `*llm.Client` via a `Chat` interface (same shape as `analyze.Analyzer` — testable with a fake).
- New `BuildSummary` orchestrator that pulls opted-in groups, fetches their unsummarized messages from the last 7 days, summarizes each group with one OpenAI call, and returns sections + the list of message IDs to mark.
- New `RenderText` / `RenderHTML` for the WhatsApp section. Plain-text + HTML, same styling vocabulary as the calendar digest.
- New `Store.OptedInGroups`, `Store.UnsummarizedMessages`, `Store.MarkSummarized` methods.
- `cmd/darek/daily_digest.go` extended: build the WhatsApp section after the calendar one, append to both `text` and `html` parts, send the email, then mark messages summarized.
- Failure isolation: a failure in the WhatsApp section logs a warning and skips that section; the calendar digest still goes out. A failure to mark-summarized after a successful send logs a warning but is not fatal (worst case: tomorrow's digest re-summarizes the same messages, paying the LLM cost twice).

### Out (deferred — separate plans or YAGNI)

- The `whatsapp.summarize_group` agent tool (chat-side). Same `Summarizer` would back it.
- The `whatsapp.send` agent tool (sub-project C).
- Read-receipt-aware filtering ("skip what you already read on phone"). User chose A in brainstorming; this is a possible follow-up.
- Per-group summary configuration (e.g. exclude noisy groups even though they are opted-in for ingest). Add later if useful.
- Configurable lookback / window size — hardcoded `lookbackDays = 7`.
- Excluding the user's own messages from the summary input. The user's contributions can be useful context (e.g. "Bart said he'd be late"). Keep simple.
- Rich attachments / inline images. Plain text and HTML only.

## 3. Architecture

The cron pod (`darek calendar daily-digest`, scheduled by the helm chart's CronJob) runs as a separate process from `darek serve`. It does **not** connect to WhatsApp — only the serve pod owns the whatsmeow socket. The cron only reads `whatsapp_groups` + `whatsapp_messages` from Postgres.

```
runDailyDigest()
  ├── existing calendar pipeline → calText, calHTML
  └── (new) whatsapp pipeline    → waText, waHTML, summarizedIDs
                                       ↓
              composed email = calendar section + whatsapp section
                                       ↓
                                send via SMTP
                                       ↓
                  on success: store.MarkSummarized(summarizedIDs)
```

Pieces:

- **`tools/whatsapp/summary.go`** — `Summarizer`, `Section`, `BuildSummary`, `RenderText`, `RenderHTML`. The `Summarizer` depends only on a `Chat` interface so it tests with a fake.
- **`tools/whatsapp/store.go`** (modify) — add `OptedInGroups`, `UnsummarizedMessages`, `MarkSummarized`.
- **`cmd/darek/daily_digest.go`** (modify) — wire the new pipeline in.
- **`db/migrations/0007_whatsapp_summarized.up.sql`** — schema change.

## 4. Schema

`db/migrations/0007_whatsapp_summarized.up.sql`:

```sql
ALTER TABLE whatsapp_messages
    ADD COLUMN summarized_at timestamptz;

CREATE INDEX idx_whatsapp_messages_unsummarized
    ON whatsapp_messages (group_jid, sent_at)
    WHERE summarized_at IS NULL;
```

The partial index keeps "give me unsummarized messages for this group" fast as total message count grows. Rows fall out of the index once they are summarized.

No `down` migration; matches the project's convention.

## 5. Store changes

Add to `tools/whatsapp/store.go`:

```go
// OptedInGroups returns groups where ingest_enabled = true, ordered by name.
// Used by BuildSummary to drive its outer loop.
func (s *Store) OptedInGroups(ctx context.Context) ([]Group, error)

// UnsummarizedMessages returns messages for the group where summarized_at IS
// NULL and sent_at >= now() - lookbackDays. Sorted ascending by sent_at so the
// LLM sees the conversation in order.
func (s *Store) UnsummarizedMessages(ctx context.Context, groupJID string, lookbackDays int) ([]Message, error)

// MarkSummarized sets summarized_at = now() for the given message IDs in one
// statement. Idempotent: messages already summarized are unchanged.
func (s *Store) MarkSummarized(ctx context.Context, ids []string) error
```

Implementation detail for `OptedInGroups`: same SQL as `Groups` but with `WHERE g.ingest_enabled = true`. Could share by adding a filter parameter to `Groups`, but a separate method reads better at call sites.

`UnsummarizedMessages`: `SELECT id, group_jid, sender_jid, sender_name, kind, body, sent_at FROM whatsapp_messages WHERE group_jid = $1 AND summarized_at IS NULL AND sent_at >= now() - ($2 || ' days')::interval ORDER BY sent_at`.

`MarkSummarized`: `UPDATE whatsapp_messages SET summarized_at = now() WHERE id = ANY($1)`. Single statement, no transaction needed.

## 6. Summarizer

```go
package whatsapp

type Chat interface {
    Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

type Summarizer struct {
    llm Chat
}

func NewSummarizer(c Chat) *Summarizer { return &Summarizer{llm: c} }

func (s *Summarizer) Summarize(ctx context.Context, groupName string, msgs []Message) (string, error)
```

System prompt:

> You are summarizing a WhatsApp group conversation. Reply with 1–3 plain-text sentences capturing key topics, decisions, and any plans or events. Do not invent facts. If the conversation is mostly pleasantries, say so briefly. Do not include a "Summary:" prefix.

User message format:

```
Group: <name>

[2026-05-04 14:23] Bart: hey did anyone see the link?
[2026-05-04 14:25] Asia: yes — looks great!
[2026-05-04 14:30] Bart: [image] this is the venue
…
```

Time format `2006-01-02 15:04` in the user's local TZ at message-send time (Postgres returns `timestamptz` already aware; format with `time.Time.Local()` then strftime). Sender prefix is `sender_name`. Body is the stored body (placeholder for media, raw text otherwise).

The transcript is truncated to **6000 chars** (matches `analyze.maxBodyChars`). If the message log exceeds that, the prompt drops earlier messages — a 6000-char tail is more relevant for "what's new" than the start of yesterday.

Returned summary is `strings.TrimSpace(content)` of the model's first choice. If the model returns empty, `Summarize` returns an error with `"empty model response"` so the orchestrator can log + skip that group rather than emit a blank section.

`Summarize` does **not** dedupe or normalize the output. The LLM is trusted to produce 1–3 sentences as instructed; if it ignores the instruction, the email will be slightly longer. Acceptable.

## 7. Orchestrator

`Section` is a small view-model:

```go
type Section struct {
    GroupName    string
    Summary      string
    MessageCount int
    FirstSentAt  time.Time
    LastSentAt   time.Time
}
```

`BuildSummary` is the orchestrator. It is a free function (not a method on a struct) because it does not need to hold state — it composes Store + Summarizer:

```go
const lookbackDays = 7

func BuildSummary(
    ctx context.Context,
    store *Store,
    summarizer *Summarizer,
) (sections []Section, summarizedIDs []string, err error)
```

Flow:

1. `groups, err := store.OptedInGroups(ctx)`. On error, return `(nil, nil, err)`.
2. For each group:
   1. `msgs, err := store.UnsummarizedMessages(ctx, g.JID, lookbackDays)`. If err, return.
   2. If `len(msgs) == 0`, skip.
   3. `summary, err := summarizer.Summarize(ctx, g.Name, msgs)`. If err, log (via stderr) + skip — one bad group doesn't kill the rest.
   4. Append `Section{...}`. Append all `msg.ID` to `summarizedIDs`.
3. Return.

Caller decides what to do with `summarizedIDs`. The cron only marks them after the email send succeeds.

A group with messages but a failed summarization is **not** included in `summarizedIDs`. Tomorrow's run will retry. A group with messages and a successful summary is included even if email-send later fails — but `MarkSummarized` is only called on email-send success, so retries work the same way for both kinds of failure.

## 8. Rendering

Two pure functions in `summary.go`:

```go
func RenderText(sections []Section) string
func RenderHTML(sections []Section) string
```

Empty input → empty string. Caller checks the return and skips appending to the email.

### Text format

```
WhatsApp — last 24h
───────────────────

▸ Family chat (12 messages, 14:02–22:48)
   Anna shared photos from the lake; Marek confirmed dinner Sunday at 19:00.

▸ Work — backend team (47 messages, 09:11–17:33)
   Discussed migration of analytics service to Go 1.25; Karol committed
   to drafting the deploy plan by Friday.
```

The first/last timestamps in the meta line use HH:MM if both are on the same day, otherwise `Mon HH:MM`. Word-wrapped at 78 columns.

The header is `WhatsApp — last 24h` even though the lookback is 7 days, because in practice messages older than 24h have already been summarized in earlier digests. If a digest run is missed, this label is technically inaccurate for one day; not worth the conditional plumbing.

### HTML format

Same vocabulary as the existing calendar digest cards (rounded surface, border, dimmed meta). Each section is a card:

```html
<section class="wa-digest">
  <h2>WhatsApp</h2>
  <div class="wa-digest-card">
    <div class="wa-digest-head">
      <strong>Family chat</strong>
      <span class="wa-digest-meta">12 messages · 14:02–22:48</span>
    </div>
    <p>Anna shared photos from the lake; Marek confirmed dinner Sunday at 19:00.</p>
  </div>
  …
</section>
```

Inline CSS in the email (the existing calendar digest renders its own CSS inline because Gmail strips `<style>` blocks in some clients). The exact CSS reuses the calendar digest's color tokens — neutral grayscale that reads on any client. ~25 lines of inline CSS appended to whatever the calendar HTML produces.

`html/template` is used for HTML output to ensure escaping (group names can contain anything).

The whole WhatsApp section appears **after** the calendar in both formats. Calendar is "must-see today", WhatsApp is "what happened recently".

## 9. Daily digest integration

`cmd/darek/daily_digest.go` gains a section after the existing calendar pipeline and before `BuildEmail`:

```go
// existing: srcs.ListEvents -> events -> digest.Group -> calText/calHTML
calText := digest.RenderText(buckets)
calHTML := digest.RenderHTML(buckets, now)

// new: WhatsApp section, only if WhatsApp is enabled AND OpenAI is configured.
var waText, waHTML string
var summarizedIDs []string
if cfg.WhatsApp.Enabled {
    apiKey, _ := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
    if apiKey != "" {
        llmClient, err := llm.New(llm.Options{
            APIKey: apiKey, BaseURL: cfg.OpenAI.BaseURL, Model: cfg.OpenAI.Model,
            Timeout: cfg.Agent.LLMTimeout,
        })
        if err != nil {
            fmt.Fprintf(os.Stderr, "warn: llm client for whatsapp summary: %v\n", err)
        } else {
            waStore := whatsapp.NewStore(pool)
            summarizer := whatsapp.NewSummarizer(llmClient)
            sections, ids, err := whatsapp.BuildSummary(ctx, waStore, summarizer)
            switch {
            case err != nil:
                fmt.Fprintf(os.Stderr, "warn: whatsapp summary failed: %v\n", err)
            case len(sections) > 0:
                waText = whatsapp.RenderText(sections)
                waHTML = whatsapp.RenderHTML(sections)
                summarizedIDs = ids
            }
        }
    }
}

text := calText
html := calHTML
if waText != "" {
    text += "\n\n" + waText
    html += waHTML
}

// existing: BuildEmail -> sender.Send
if err := sender.Send(ctx, mailAcct.Email, []string{d.To}, raw); err != nil {
    return fmt.Errorf("send digest: %w", err)
}

// new: only mark summarized AFTER the email is in the wire.
if len(summarizedIDs) > 0 {
    waStore := whatsapp.NewStore(pool)
    if err := waStore.MarkSummarized(ctx, summarizedIDs); err != nil {
        fmt.Fprintf(os.Stderr, "warn: mark whatsapp summarized: %v\n", err)
    }
}
```

`runDailyDigest` does **not** currently open a Postgres connection (it only reads calendar sources + sends SMTP). Add a `db.Open(ctx, dsn)` near the top, alongside the existing OTEL init, gated on `cfg.WhatsApp.Enabled` — if WhatsApp isn't enabled, skip the DB open entirely so a calendar-only deployment still works without a `DAREK_POSTGRES_URL`. The `dsn` resolves the same way as in other commands: `config.ResolveSecret("env:" + cfg.Postgres.URLEnv)`.

## 10. Configuration

No new config block. The feature is gated by:

- `whatsapp.enabled: true` (already exists, set in `helm/darek/values/darek.yaml`).
- `openai.api_key_env` resolves to a non-empty value (already required by the agent).

If either is missing, the WhatsApp section is silently omitted from the email. The existing `calendar_digest.to` / `calendar_digest.from_account` cover the recipient.

## 11. Observability

Reuse existing instruments:

- The summarizer's `*llm.Client.Chat` calls are already wrapped by `obs.Dep("openai_chat", "chat", …)` → tokens, cost, latency flow into existing `darek.tokens.*`, `darek.llm.cost_usd`, `darek.llm.latency`, `darek.dep.*` counters.
- `darek.whatsapp.messages_ingested` counter from sub-project A is unrelated and stays as-is.

One new counter (optional, low-priority): `darek.whatsapp.summary` with `outcome ∈ {ok, error, skipped}`. Decide during implementation; if the existing dep metrics already give us "did the LLM call succeed", a summary-specific counter is redundant. Default: skip.

## 12. Testing

Unit tests in `tools/whatsapp/summary_test.go` (no integration tag — pure logic):

- `Summarizer` happy path: stub `Chat` returns a known string. Inspect captured `params.Messages` to confirm the user message contains `Group: …`, the time-prefixed message lines, and is truncated when transcript exceeds 6000 chars. Returned summary equals the model output trimmed.
- `Summarizer` empty model response: `Chat` returns choices with empty content → `Summarize` returns error.
- `Summarizer` LLM error: returns error.
- `RenderText` golden test: two sections → known string output. Includes time-range formatting variants (same-day vs cross-day).
- `RenderText` empty input → `""`.
- `RenderHTML` golden test (substring assertions, not full byte equality, since whitespace variation is fragile): contains `<h2>WhatsApp</h2>`, group name in `<strong>`, summary in `<p>`.
- `RenderHTML` escapes hostile group names (e.g. `<script>alert(1)</script>` → escaped).

Integration tests in `tools/whatsapp/summary_integration_test.go` (build tag `integration`, pg testcontainer):

- `Store.OptedInGroups` filters out `ingest_enabled = false` rows.
- `Store.UnsummarizedMessages` returns rows with `summarized_at IS NULL` and within lookback; excludes older or already-summarized rows.
- `Store.MarkSummarized` flips `summarized_at` for the listed IDs only; idempotent.
- `BuildSummary` end-to-end: with two opted-in groups (one with messages, one without), returns one section + the message IDs from the active group. With a stub `Summarizer`, no LLM call goes out.
- `BuildSummary` with a failing `Summarizer` for one group: returns the other group's section, doesn't include failed-group IDs in `summarizedIDs`.

End-to-end smoke test (manual):

1. Apply migration in the running cluster (helm pre-install / pre-upgrade Job already runs migrations).
2. Trigger the cron manually: `kubectl -n darek create job --from=cronjob/darek-daily-digest digest-test`.
3. Open the resulting email; verify both calendar and WhatsApp sections are present and correctly formatted.
4. Trigger again immediately. The WhatsApp section should be empty (or absent) because all messages are now summarized.

## 13. Risks / edge cases

- **Cost.** Each opted-in group with new messages → one OpenAI call per day. Per-call cost is bounded by the 6000-char transcript cap (~1500 input tokens + ~150 output) — call it under $0.005 per group on the cheap model. With 10 active groups, $1.50/month worst case.
- **Hallucination.** The LLM may invent details. The system prompt explicitly says "do not invent facts"; we trust the model on a daily digest where the user can correct course (and the summary is a low-stakes "what happened" not a decision aid).
- **Stale messages.** A 7-day lookback means if darek serve was offline for 8+ days, the oldest messages from that gap are missed. Acceptable; the user can re-pair / re-sync via WhatsApp's history if it ever matters.
- **Mark-summarized after partial-success email.** SMTP `Send` returns success after the SMTP server queues the message — actual delivery to the user's inbox can still fail downstream. We treat SMTP success as authoritative. If the message bounces, those WhatsApp messages were nonetheless marked summarized; user loses them in tomorrow's digest. Acceptable.
- **Concurrent mark-summarized vs ingest.** A new message arriving during the digest run could land in Postgres while we're iterating. The new message will not be in our `UnsummarizedMessages` snapshot (Postgres MVCC), so it will not be marked, and tomorrow's digest will pick it up. Correct behavior.
- **Group renamed in WhatsApp.** Our `whatsapp_groups.name` is updated on `RefreshGroups` only. The summary uses whatever name we have. If the user renames a group between refreshes, the email shows the old name for a day. Tolerable.
- **Empty digest day.** If no groups have new messages AND there are no calendar events for the next 3 days, the email is essentially blank. `BuildEmail` still sends it. Acceptable — better than asking the user to remember to check whether it ran.

## 14. Out of scope (recap)

- Read-receipt-aware filtering ("skip what you read on phone").
- Excluding own messages from the input.
- Configurable lookback / window per user or per group.
- Per-group summary toggles (separate from ingest opt-in).
- Agent tool `whatsapp.summarize_group` (chat-side). Same `Summarizer` would back it.
- `whatsapp.send` agent tool (sub-project C).
- Rich attachments (inline images / voice transcription).
