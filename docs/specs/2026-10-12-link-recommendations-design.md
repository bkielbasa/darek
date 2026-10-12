# Darek — link analysis & recommendations (design)

**Date:** 2026-10-12
**Status:** draft (awaiting review)
**Author:** brainstormed with opencode

## 0. Position in the larger plan

Darek already ingests and rates reading material:

- **`links` table** (`db/migrations/0003_links.up.sql` … `0005_links_summary.up.sql`) — one row per article/video/…, with explicit 1–5 `rating`, `summary`, `tags`, `feed`, `kind`, `created_at`, a tsvector `search` column, `analyzed_at`, and an **unused `embedding vector(1536)`** column (pgvector 0.8.2 enabled by `0003`).
- **Ingestion** — `freshrssimport.Sync` (and `todoistimport`) pull unread items through `links.IngestOne`. Only **videos** are auto-analyzed today (`freshrssimport.OnVideoIngestedFunc` → `analyze.VideoAwareAnalyzer` → `links.Store.ApplyAnalysis`).
- **Rating** — the web UI (`cmd/darek/serve`) and the `links.save` agent tool set `rating`.
- **Daily digest** — `darek calendar daily-digest` (`cmd/darek/daily_digest.go`) already composes calendar → WhatsApp summary → cross-digest memory sections.

Two gaps motivate this design:

1. **Articles are stored without AI summary or tags.** Only 9/599 links are analyzed; tags are effectively empty, so there is nothing to personalize on.
2. **No discovery.** Darek collects hundreds of unread links but never says which ones to actually read.

This spec delivers **(A) automatic summary + tags for every link when it is loaded** and **(B) a recommendation system** that learns from ratings and surfaces up to 3 unread links each morning — in the digest and via chat. A is the enabler; B consumes it.

## 1. Goals

**A. Auto-analysis.** When a link is ingested (freshrss/todoist), generate a concise summary and 3–7 tags with the existing analyzer and persist them; provide a backfill command for the existing backlog.

**B. Recommendations.** Given the user's ratings, produce up to N (default 3) suggestions of **unrated** links to read each day, each with a one-line reason, in the daily digest and via a `links.recommend` chat tool. Remembered so suggestions are never repeated.

## 2. Data reality (measured 2026-10-12)

| Metric | Value |
|---|---|
| Links | 599 (455 `video`, 144 `article`) |
| Rated | 38 (`>=4`: 14, `<=2`: 17, `=3`: 7) |
| Unrated | 561 |
| Unrated in last 30 days | 321 |
| Have `summary` | 593 |
| LLM-analyzed (`analyzed_at` set) | 9 (tags sparse) |
| Have `embedding` | 0 |

Consequences: the analysis backfill is the highest-value first step (gives every item tags + a clean summary), the explicit signal is small (38 ratings), and videos vastly outnumber articles.

## 3. Part A — automatic analysis on ingest

Today the ingest callback is video-only and title-only. Generalize it:

- **Callback contract.** Rename `freshrssimport.OnVideoIngestedFunc` → `OnIngestedFunc`:
  `func(ctx context.Context, linkID uuid.UUID, url, title, body string) error`
  (body = the source-provided, HTML-stripped summary). `todoistimport` gets the same rename.
- **Trigger.** Call it for **every newly-ingested link whose `kind` is in `analyze.kinds`** (default `[article, video]`) instead of only `kind=="video"`.
- **Analyzer.** `analyze.VideoAwareAnalyzer` already handles both: YouTube URLs → transcript becomes the body; everything else → the passed body (source summary). For non-YouTube articles, the FreshRSS `Article.Summary` (already stripped) is a good-enough body. Fetching the full web page is deferred (Section 12).
- **Controlled vocabulary (reuse existing tags).** Tags should reinforce an existing vocabulary rather than spawn near-duplicates (`go` vs `golang` vs `go-lang`). Before analysis, fetch the current vocabulary with `links.Store.TagVocabulary(ctx, limit)` (`SELECT tag, count(*) FROM links, unnest(tags) tag GROUP BY 1 ORDER BY 2 DESC LIMIT $n`), pass it into the analyzer via a new `analyze.Input.KnownTags []string`, and instruct the prompt to **reuse an existing tag whenever it fits**, inventing a new one only when nothing matches. After generation, `normalizeTags` canonicalizes case-insensitively against the known set (so `Go` → the existing `go`) before the result is persisted.
- **Persist.** `links.Store.ApplyAnalysis(id, summary, tags)` overwrites `summary` and unions `tags`, bumping `analyzed_at`. Analysis runs inline per item (bounded worker pool already exists in `Sync`), best-effort — failures are appended to `Result.Errors` and never abort the sync.
- **Config.** New `analyze.enabled` (opt-in; default false) and `analyze.kinds` (default `[article, video]`). Enabling also turns on the previously-implicit video analysis. A `analyze.tag_vocab_limit` (default 200) bounds the vocabulary handed to the model. Requires OpenAI; without a key, analysis is skipped with an `info:` log (unchanged behavior).
- **Backfill.** New `darek links analyze [--limit N] [--kind article] [--force]` command processes links where `analyzed_at IS NULL` (or all, with `--force`) via a new `links.Store.PendingAnalysis(ctx, kind, limit)` iterator. This brings the 590 un-analyzed rows up to date and can be re-run anytime.

## 4. Part B — recommendation approach (hybrid retrieval + LLM re-rank)

Two stages, optimizing different things:

1. **Retrieval (recall)** — pgvector cosine similarity against a **liked centroid** minus a **disliked centroid**, over the entire unrated backlog. Fast; sees every candidate.
2. **Selection (precision)** — one LLM call ranks a bounded candidate list (default 30) against a readable taste profile and returns up to N picks with reasons.

Rationale: keyword (tsvector) similarity misses semantic matches; pure LLM over the whole backlog is impossible (context) and over a small window has poor recall with only 38 ratings. Embeddings need no training, so they work immediately — and Part A makes them much better by giving every link tags + a clean summary.

**Graceful degradation:**

- No embeddings / fewer than `min_liked` (default 3) liked items → tsvector retrieval (query built from liked titles+tags), no centroid.
- LLM selection fails → fall back to the top retrieval-scored candidates (no reasons).

## 5. Data model

### 5.1 `links` (existing, extended)

```sql
ALTER TABLE links ADD COLUMN embedded_at timestamptz;
CREATE INDEX links_embedding_hnsw ON links USING hnsw (embedding vector_cosine_ops);
CREATE INDEX links_analyzed_pending ON links (kind, created_at) WHERE analyzed_at IS NULL;
```

- `embedded_at` marks successful embedding → backfill finds new rows (`WHERE embedded_at IS NULL`) and never re-embeds.
- HNSW (`vector_cosine_ops`) powers `<=>`. A seq scan is fine at 599 rows; the index future-proofs.
- Embedding text = `coalesce(title,'') || ' ' || coalesce(summary,'') || ' ' || tags || ' ' || coalesce(feed,'')`, falling back to the URL when empty.

### 5.2 `link_recommendations` (new)

```sql
CREATE TABLE link_recommendations (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id        uuid NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    recommended_at timestamptz NOT NULL DEFAULT now(),
    kind           text NOT NULL DEFAULT '',
    score          real NOT NULL DEFAULT 0,
    reason         text NOT NULL DEFAULT '',
    source         text NOT NULL DEFAULT 'digest'
);
CREATE UNIQUE INDEX link_recommendations_link_uniq ON link_recommendations (link_id);
CREATE INDEX link_recommendations_at ON link_recommendations (recommended_at DESC);
```

One row per link (`UNIQUE(link_id)`). `source` ∈ `digest|chat`. `cooldown_days` allows a future re-suggest policy once rows age out.

## 6. Part B architecture & flow

```
freshrss sync ──► IngestOne ──► OnIngestedFunc ──► analyze ──► ApplyAnalysis (summary+tags)
daily_digest  ─┐
links.recommend┼─► recommend.Build
               │     1. EnsureEmbeddings  (links.embedded_at IS NULL → llm.Embed → links.embedding)
               │     2. BuildProfile      (rating>=4 / <=2 → centroids)
               │     3. Candidates        (per-kind vector retrieval, or tsvector fallback)
               │     4. Selector.Select   (one LLM call → up to N picks + reasons)
               │     5. SaveRecommendations
               └─► render text/html / tool string
```

## 7. Package layout

### 7.1 `links` (Part A additions)

- `links.Store.PendingAnalysis(ctx, kind string, limit int) ([]Link, error)` — `WHERE analyzed_at IS NULL [AND kind=$k] ORDER BY created_at DESC LIMIT $n`.
- `links.Store.TagVocabulary(ctx, limit int) ([]string, error)` — most-used tags, descending; powers tag reuse.
- `links.Store.ApplyAnalysis` already exists.

### 7.1a `analyze` (Part A additions)

- `analyze.Input` gains `KnownTags []string`.
- `Analyzer.Analyze` injects the known tags into the prompt: *"Prefer reusing these existing tags when they fit: …; only add a new tag when none match."*
- `normalizeTags` gains a canonicalization pass against `KnownTags` (case-insensitive exact match).

### 7.2 `recommend/store.go`

Local types (avoids importing `links`):

```go
type EmbedText struct { ID uuid.UUID; Text string }
type Example struct { ID uuid.UUID; Title, Kind, Feed, Summary string; Rating int; Embedding []float32 }
type Candidate struct { ID uuid.UUID; URL, Title, Kind, Feed, Summary string; Score float32 }
type Suggestion struct { Candidate; Reason string }
```

Methods:

- `PendingEmbeddings(ctx, limit int) ([]EmbedText, error)`
- `SetEmbedding(ctx, id, vec []float32) error` — vector passed as a string (`[a,b,c]`) cast to `::vector` — **no new Go dependency**.
- `RatedExamples(ctx, minRating, maxRating, limit int) ([]Example, error)`.
- `VectorCandidates(ctx, liked, disliked []float32, lookbackDays, cooldownDays, perKind int) ([]Candidate, error)`.
- `TextCandidates(ctx, query string, lookbackDays, cooldownDays, perKind int) ([]Candidate, error)`.
- `SaveRecommendations(ctx, picks []Suggestion, source string) error`.

Retrieval core (run once per `kind`):

```sql
SELECT id, url, coalesce(title,''), kind, coalesce(feed,''), coalesce(summary,''),
       (embedding <=> $1::vector) - 0.5*(embedding <=> $2::vector) AS score
  FROM links l
 WHERE l.rating IS NULL
   AND l.embedding IS NOT NULL
   AND l.kind = $3
   AND ($4 = 0 OR l.created_at >= now() - make_interval(days => $4))
   AND NOT EXISTS (
       SELECT 1 FROM link_recommendations r
        WHERE r.link_id = l.id
          AND ($5 = 0 OR r.recommended_at >= now() - make_interval(days => $5)))
 ORDER BY score ASC
 LIMIT $6
```

`perKind = ceil(candidate_pool / 2)`; results interleaved (article, video, article…) then truncated to `candidate_pool`.

### 7.3 `recommend/embed.go`

```go
type Embedder interface { Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) }
func EnsureEmbeddings(ctx, store *Store, e Embedder, model string, limit, batchSize int) (int, error)
```

Backed by a new `(*llm.Client).Embed(ctx, model string, inputs []string) ([][]float32, error)` using `openai.Client.Embeddings.New` (`EmbeddingNewParams{Model, Input: EmbeddingNewParamsInputUnion{OfArrayOfStrings: inputs}}`; response `CreateEmbeddingResponse.Data[i].Embedding []float64`).

### 7.4 `recommend/profile.go`

```go
type Profile struct { Liked, Disliked []Example; LikedCentroid, DislikedCentroid []float32 }
func BuildProfile(ctx, store *Store) (Profile, error)
func centroid(vecs [][]float32) []float32   // mean then L2-normalize
```

Liked = rating ≥ 4, Disliked = rating ≤ 2. If fewer than `min_liked` liked items (or no embeddings), `LikedCentroid` is nil → text fallback.

### 7.5 `recommend/select.go`

```go
type Chat interface { Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) }
type Selector struct { llm Chat }
func (s *Selector) Select(ctx, profile Profile, cands []Candidate, count int) ([]Suggestion, error)
```

JSON mode; model returns `{"recommendations":[{"index":<int>,"reason":"<one line>"}]}`. The user message carries a **taste profile** (liked/disliked titles, kind, feed, short summary) and a numbered candidate list `[i] kind | feed | title — summary`. Prompt instructs: pick up to `count`; prefer variety (**at least one article and one video when reasonable candidates exist**); never invent URLs; give a short concrete reason. `parsePicks` strips code fences (same shape as `memory.parseCandidates`), drops out-of-range/duplicate indexes, caps at `count`.

### 7.6 `recommend/recommend.go`

```go
type Options struct { Count, CandidatePool, LookbackDays, CooldownDays, MinLiked, EmbedLimit, EmbedBatch int; EmbeddingModel string }
type Result struct { Suggestions []Suggestion; Embedded, Candidates int; Vector bool }
func Build(ctx, store *Store, emb Embedder, sel *Selector, opt Options, source string, logf func(string, ...any)) (Result, error)
```

Steps: `EnsureEmbeddings` → `BuildProfile` → vector or text candidates → `Select` (fallback to top-scored on error/empty) → `SaveRecommendations`. Per-stage failures log and degrade; only the initial fetches abort on DB error.

### 7.6 `recommend/render.go`

`RenderText` / `RenderHTML` — "Reading suggestions" section, mirroring `memory/render.go` (empty → `""`, `htmlEscape`, inline-styled cards):

```
Reading suggestions
───────────────────

▸ [article] Title here — feed
  Why: one-line reason
  https://example.com/x
```

### 7.7 `recommend/tool.go`

`links.recommend` agent tool. Args: `count` (1–10, default 3), `kind` (optional), `include_seen` (bool, default false). Runs `Build(source: "chat")` and returns rendered text.

## 8. Config

```yaml
analyze:
  enabled: false                # analyze newly-ingested links (summary + tags)
  kinds: [article, video]       # kinds auto-analyzed on ingest
  tag_vocab_limit: 200          # existing tags handed to the model for reuse

recommend:
  enabled: false                # opt-in: costs embeddings + one LLM call per digest
  count: 3
  candidate_pool: 30
  lookback_days: 0              # 0 = any age
  cooldown_days: 0              # 0 = never repeat
  min_liked: 3                  # below this → text retrieval
  embedding_model: text-embedding-3-small
  embed_batch: 64
  embed_limit: 256              # max links embedded per run
```

`config.Config` gains `Analyze Analyze` and `Recommend Recommend`. The digest opens Postgres when `whatsapp.enabled || recommend.enabled`.

## 9. Commands

- `darek freshrss sync` / `darek todoist …` — now also analyze newly-ingested article/video links when `analyze.enabled`.
- `darek links analyze [--limit N] [--kind article] [--force]` — backfill summary + tags for existing links (the 590 backlog).
- `darek links embed [--limit N]` — backfill embeddings for links missing them.

## 10. Digest integration

`cmd/darek/daily_digest.go` gains a `recommend` block mirroring the memory block. Section order:

```
calendar → Reading suggestions → Follow-ups/People → WhatsApp
```

Failures are logged (`info:`/`warn:`) and skipped — never blocking the calendar email. Picks are persisted before sending (a failed send can't cause repeats next day; content is re-rendered from the just-saved picks).

## 11. Observability

New counters in `obs/metrics.go`: `darek.recommend.runs`, `darek.recommend.suggestions` (label `source`), `darek.recommend.embeddings`. `llm.Embed` records latency under the existing LLM metrics; the existing `darek.links.analyze` counter gains a `trigger="sync"` label value for article analysis.

## 12. Risks & mitigations

| Risk | Mitigation |
|---|---|
| Analysis cost/latency during sync | Best-effort, bounded worker pool; `analyze.enabled` opt-in; failures non-fatal. |
| Source summary is short / truncated | Good enough for tags; full-page extraction deferred. |
| Cold start (few ratings) | `min_liked` gate → text fallback; negative centroid still helps. |
| Embedding dimension drift | Column is `vector(1536)`; a model change needs a migration + re-embed. |
| Repeat suggestions | `link_recommendations` unique per link; `cooldown_days` configurable. |
| Video/Article imbalance (455:144) | Per-kind retrieval + balanced interleave + prompt instruction. |
| Stale backlog surfaced | `lookback_days` configurable (default 0 = any). |
| Un-embeddable rows | Fall back to URL text; `embedded_at` prevents re-embedding. |

## 13. Out of scope (deferred)

- Full-article HTML extraction (readability) for the analyzer body.
- Collaborative filtering; implicit signals (clicks, dwell, "read").
- Per-feature attribution beyond the one-line reason.
- A dedicated recommendations web-UI page.
- A feedback loop that learns from opened/ignored suggestions.
- Model training / reranking.

## 14. Open questions

1. Digest section order (proposed: after calendar, before Follow-ups/People).
2. Is `count: 3` the right default?
3. Should the chat tool consume from the same never-repeat pool as the digest (proposed: yes)?
4. Should analysis also cover `podcast`/`tweet` kinds by default (proposed: no, article+video only)?
