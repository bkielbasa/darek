# Link Analysis & Recommendations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** (A) Automatically generate a summary + tags (reusing an existing tag vocabulary) for each link when it is ingested, with a backfill command; and (B) build a rating-driven recommendation system that surfaces up to 3 unread links each day in the digest and via chat, without repeating suggestions.

**Architecture:** Part A generalizes the video-only ingest callback to `OnIngestedFunc` (called for every new link, carrying `kind`+`body`), adds `analyze.Input.KnownTags` for tag reuse, and adds `links.Store.TagVocabulary`/`PendingAnalysis`. Part B adds a `recommend` package doing pgvector cosine retrieval (liked minus disliked centroid) → one LLM re-rank call → persist to `link_recommendations`, rendered into the digest and exposed as `links.recommend`. Embeddings via a new `llm.Embed`.

**Tech Stack:** Existing Go stack (pgx v5, openai-go v1.12.0, pgvector 0.8.2). No new direct dependencies — vectors are passed to Postgres as `'[..]'::vector`, and embeddings use `openai.Client.Embeddings.New`. Tests use `github.com/stretchr/testify/require` and the pg testcontainer helper (`internal/testutil/pg`).

**Design source:** [docs/specs/2026-10-12-link-recommendations-design.md](../specs/2026-10-12-link-recommendations-design.md) (draft 2026-10-12).

**Out of scope (deferred):** full-article HTML extraction; collaborative filtering; implicit signals; recommendations web-UI page; feedback loop; reranking model.

---

## File Map

| Path | Responsibility |
|---|---|
| `links/store.go` | (modify) add `TagVocabulary`, `PendingAnalysis`. |
| `links/store_test.go` | (modify) integration tests for the two methods. |
| `analyze/analyze.go` | (modify) `Input.KnownTags`, prompt reuse, `normalizeTags` canonicalization. |
| `analyze/analyze_test.go` | (modify) unit tests for tag reuse + canonicalization. |
| `freshrssimport/sync.go` | (modify) `OnIngestedFunc` (kind+body), call for every new link. |
| `freshrssimport/sync_test.go` | (modify) renamed/updated callback tests. |
| `todoistimport/sync.go` | (modify) same as freshrss. |
| `todoistimport/sync_test.go` | (modify) renamed/updated callback tests. |
| `config/types.go` | (modify) `Analyze`, `Recommend` structs. |
| `cmd/darek/auto_analyze.go` | (modify) `buildAnalyzer`, `buildAutoAnalyze` (kinds+vocab). |
| `cmd/darek/freshrss.go` | (modify) use new callback. |
| `cmd/darek/todoist.go` | (modify) use new callback. |
| `cmd/darek/links.go` | (create) `darek links analyze` / `darek links embed`. |
| `cmd/darek/main.go` | (modify) dispatch `links`. |
| `db/migrations/0016_link_recommendations.up.sql` | (create) `embedded_at`, indexes, `link_recommendations`. |
| `llm/client.go` | (modify) add `Embed`. |
| `llm/client_test.go` | (modify/create) unit test for `Embed`. |
| `recommend/store.go` | (create) DB access. |
| `recommend/store_integration_test.go` | (create) integration tests. |
| `recommend/embed.go` | (create) `Embedder`, `EnsureEmbeddings`. |
| `recommend/embed_test.go` | (create) unit tests (fake embedder). |
| `recommend/profile.go` | (create) `Profile`, `BuildProfile`, `centroid`, candidate assembly. |
| `recommend/candidates_integration_test.go` | (create) integration tests. |
| `recommend/select.go` | (create) `Chat`, `Selector`, prompt, `parsePicks`. |
| `recommend/select_test.go` | (create) unit tests. |
| `recommend/render.go` | (create) `RenderText`, `RenderHTML`. |
| `recommend/render_test.go` | (create) unit tests. |
| `recommend/recommend.go` | (create) `Options`, `Result`, `Build`. |
| `recommend/recommend_integration_test.go` | (create) integration tests. |
| `recommend/tool.go` | (create) `links.recommend` tool. |
| `recommend/tool_test.go` | (create) tool test. |
| `obs/metrics.go`, `obs/metrics_test.go` | (modify) recommend counters. |
| `cmd/darek/daily_digest.go` | (modify) add the suggestions section. |
| `cmd/darek/chat.go` | (modify) register `recommend.ListTool`. |
| `README.md` | (modify) document analysis + recommendations. |

---

# Part A — automatic analysis on ingest

## Task A1 — `links` store: tag vocabulary + pending analysis (TDD, integration)

**Files:**
- Modify: `links/store.go`
- Modify: `links/store_test.go`

- [ ] **Step 1: Write failing tests**

Append to `links/store_test.go`:

```go
func TestStore_TagVocabulary(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	r5 := 5
	for _, in := range []SaveInput{
		{URL: "https://e.com/1", Title: "a", Tags: []string{"go", "concurrency"}, Rating: &r5},
		{URL: "https://e.com/2", Title: "b", Tags: []string{"go", "databases"}},
		{URL: "https://e.com/3", Title: "c", Tags: []string{"go"}},
	} {
		_, err := s.Save(ctx, in)
		require.NoError(t, err)
	}

	tags, err := s.TagVocabulary(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"go", "concurrency", "databases"}, tags, "ordered by usage desc, then name")
}

func TestStore_PendingAnalysis(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	id, err := s.Save(ctx, SaveInput{URL: "https://example.com/v", Title: "V", Source: "freshrss"})
	require.NoError(t, err)
	_, err = s.Save(ctx, SaveInput{URL: "https://example.com/a", Title: "A", Kind: "article", Source: "freshrss"})
	require.NoError(t, err)
	_, err = s.Save(ctx, SaveInput{URL: "https://example.com/done", Title: "D"})
	require.NoError(t, err)
	require.NoError(t, s.ApplyAnalysis(ctx, func() uuid.UUID { var u uuid.UUID; require.NoError(t, s.pool.QueryRow(ctx,
		`SELECT id FROM links WHERE url='https://example.com/done'`).Scan(&u)); return u }(), "s", []string{"t"}))

	all, err := s.PendingAnalysis(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, all, 2, "analyzed rows excluded")

	arts, err := s.PendingAnalysis(ctx, "article", 10)
	require.NoError(t, err)
	require.Len(t, arts, 1)
	require.Equal(t, "article", arts[0].Kind)

	_ = id
}
```

(Import `github.com/google/uuid` in the test if not already.)

- [ ] **Step 2: Run, expect failure**

`go test -tags integration ./links/ -run "TestStore_TagVocabulary|TestStore_PendingAnalysis" -v` → FAIL (undefined).

- [ ] **Step 3: Implement** (append to `links/store.go`)

```go
// TagVocabulary returns the most-used tags across all links, descending by
// usage then alphabetically. Used to steer the analyzer toward reusing tags.
func (s *Store) TagVocabulary(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT tag
		  FROM links, unnest(tags) AS tag
		 WHERE tag <> ''
		 GROUP BY tag
		 ORDER BY count(*) DESC, tag ASC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PendingAnalysis returns links that have not been analyzed yet, newest first.
// kind "" means all kinds.
func (s *Store) PendingAnalysis(ctx context.Context, kind string, limit int) ([]Link, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + linkColumns + ` FROM links WHERE analyzed_at IS NULL`
	args := []any{}
	if kind != "" {
		args = append(args, kind)
		q += fmt.Sprintf(" AND kind = $%d", len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinks(rows)
}
```

**Import note:** the existing column lists are inline in `Search`/`Get`. Factor the common column list into a `const linkColumns = "id, url, coalesce(title,''), rating, tags, coalesce(notes,''), source, kind, coalesce(feed,''), coalesce(summary,''), analyzed_at, created_at, updated_at"` and reuse it in `Search`, `Similar` (+rank), `Get`, and `PendingAnalysis`. Keep `Similar`'s extra `ts_rank(...) AS rank` appended.

- [ ] **Step 4: Run, expect pass** — `go test -tags integration ./links/ -v`.

- [ ] **Step 5: Commit** — `feat(links): tag vocabulary + pending-analysis queries`.

---

## Task A2 — Analyzer tag reuse (TDD, unit)

**Files:** `analyze/analyze.go`, `analyze/analyze_test.go`.

- [ ] **Step 1: Extend `Input` and the prompt**

```go
type Input struct {
	Title     string
	URL       string
	Body      string
	KnownTags []string // existing vocabulary; the model should reuse these when they fit
}
```

In `Analyze`, build the known-tag instruction and append it to the system message:

```go
sys := systemPrompt
if len(in.KnownTags) > 0 {
	sys += "\nPrefer reusing these existing tags when they fit (do not create near-duplicates): " +
		strings.Join(in.KnownTags, ", ") + "."
}
```

And canonicalize the result:

```go
return Output{
	Summary: strings.TrimSpace(raw.Summary),
	Tags:    canonicalize(normalizeTags(raw.Tags), in.KnownTags),
}, nil
```

```go
// canonicalize maps tags to the exact casing/spelling of the known vocabulary
// (case-insensitive, hyphen/underscore-insensitive). Unknown tags pass through.
func canonicalize(tags, known []string) []string {
	index := make(map[string]string, len(known))
	for _, k := range known {
		index[key(k)] = k
	}
	out := make([]string, 0, len(tags))
	seen := map[string]struct{}{}
	for _, t := range tags {
		c := t
		if k, ok := index[key(t)]; ok {
			c = k
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

func key(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(s)
}
```

- [ ] **Step 2: Tests** (append to `analyze/analyze_test.go`)

```go
func TestAnalyze_ReusesKnownTags(t *testing.T) {
	chat := &fakeChat{resp: `{"summary":"s","tags":["Go","golang","Concurrency"]}`}
	a := New(chat)
	out, err := a.Analyze(context.Background(), Input{
		Title: "x", KnownTags: []string{"go", "concurrency"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"go", "concurrency"}, out.Tags, "Go→go dup collapsed; golang kept; Concurrency→concurrency")
	require.Contains(t, chat.gotSystem, "Prefer reusing these existing tags")
}

func TestCanonicalize(t *testing.T) {
	require.Equal(t, []string{"go", "systems-programming"},
		canonicalize([]string{"Go", "go", "systems_programming"},
			[]string{"go", "systems-programming"}))
}
```

(If `fakeChat` doesn't record the system message, add a `gotSystem string` field populated in `Chat` from `p.Messages[0].OfSystem.Content.OfString.Value`.)

- [ ] **Step 3: Run** — `go test ./analyze/ -run "TestAnalyze|TestCanonicalize" -v` → PASS.
- [ ] **Step 4: Commit** — `feat(analyze): reuse known tag vocabulary`.

---

## Task A3 — Generalize import callbacks (TDD, integration)

**Files:** `freshrssimport/sync.go` + `sync_test.go`, `todoistimport/sync.go` + `sync_test.go`.

- [ ] **Step 1: New callback contract** (both packages)

```go
// OnIngestedFunc is called once per newly-ingested link (any kind). The
// callback decides whether to analyze based on kind; errors are collected but
// never abort the sync.
type OnIngestedFunc func(ctx context.Context, linkID uuid.UUID, kind, url, title, body string) error
```

- [ ] **Step 2: Change the trigger**

In `processArticle`/`processTask`, replace:

```go
if isNew && kind == "video" && onVideoIngested != nil {
	if cbErr := onVideoIngested(ctx, id, a.URL, a.Title); cbErr != nil { ... }
}
```

with:

```go
if isNew && onIngested != nil {
	if cbErr := onIngested(ctx, id, kind, a.URL, a.Title, a.Summary); cbErr != nil { ... }
}
```

Rename the `Sync` parameter and `processX` parameter from `onVideoIngested` to `onIngested` throughout.

- [ ] **Step 3: Update tests** — rename `TestSync_OnVideoIngested_*` to `TestSync_OnIngested_*` and assert the new behavior:
  - New item of any kind → callback called once with the right `kind`/`body`.
  - Existing item → callback not called.
  - Callback error → recorded in `Result.Errors`, run continues.
  - `nil` callback → no panic.

- [ ] **Step 4: Run** — `go test ./freshrssimport/ ./todoistimport/ -v` → PASS.
- [ ] **Step 5: Commit** — `refactor(import): analyze every newly-ingested link, not just videos`.

---

## Task A4 — Config + wiring + `darek links analyze` (TDD light)

**Files:** `config/types.go`, `cmd/darek/auto_analyze.go`, `cmd/darek/freshrss.go`, `cmd/darek/todoist.go`, `cmd/darek/links.go` (new), `cmd/darek/main.go`.

- [ ] **Step 1: Config**

```go
type Analyze struct {
	Enabled       bool     `yaml:"enabled"`
	Kinds         []string `yaml:"kinds"`           // default [article, video]
	TagVocabLimit int      `yaml:"tag_vocab_limit"` // default 200
}

// KindSet returns the configured kinds as a set, defaulting to article+video.
func (a Analyze) KindSet() map[string]bool {
	kinds := a.Kinds
	if len(kinds) == 0 {
		kinds = []string{"article", "video"}
	}
	set := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		set[k] = true
	}
	return set
}

func (a Analyze) VocabLimit() int {
	if a.TagVocabLimit <= 0 {
		return 200
	}
	return a.TagVocabLimit
}
```

Add `Analyze Analyze` to `Config`.

- [ ] **Step 2: `cmd/darek/auto_analyze.go`**

Rename `buildVideoAnalyzer` → `buildAnalyzer` (return unchanged `*analyze.VideoAwareAnalyzer`); replace `buildVideoAutoAnalyze` with:

```go
func buildAutoAnalyze(va *analyze.VideoAwareAnalyzer, store *links.Store, kinds map[string]bool, vocab []string) freshrssimport.OnIngestedFunc {
	if va == nil {
		return nil
	}
	return func(ctx context.Context, id uuid.UUID, kind, url, title, body string) error {
		if !kinds[kind] {
			return nil
		}
		out, err := va.Analyze(ctx, analyze.Input{Title: title, URL: url, Body: body, KnownTags: vocab})
		if err != nil {
			recordSyncAnalyze(ctx, "error")
			return err
		}
		if err := store.ApplyAnalysis(ctx, id, out.Summary, out.Tags); err != nil {
			recordSyncAnalyze(ctx, "error")
			return err
		}
		recordSyncAnalyze(ctx, "ok")
		return nil
	}
}
```

> `todoistimport.OnIngestedFunc` is a distinct named type; to satisfy both, keep `buildAutoAnalyze` returning the freshrss type and wrap it (`todoistimport.OnIngestedFunc(fn)`) at the todoist call site, or define the closure inline with each package's type. Prefer a small shared helper written twice (2 lines) to avoid an import cycle.

Update `recordSyncAnalyze` trigger label from `"sync_video"` to `"sync"`.

- [ ] **Step 3: Wire `freshrss.go` / `todoist.go`**

Replace:
```go
va := buildVideoAnalyzer(cfg)
onVideo := buildVideoAutoAnalyze(va, store)
res, err := freshrssimport.Sync(ctx, fr, store, onVideo)
```
with:
```go
var onIngested freshrssimport.OnIngestedFunc
if cfg.Analyze.Enabled {
	va := buildAnalyzer(cfg)
	vocab, verr := store.TagVocabulary(ctx, cfg.Analyze.VocabLimit())
	if verr != nil {
		fmt.Fprintf(os.Stderr, "warn: tag vocabulary: %v\n", verr)
	}
	onIngested = buildAutoAnalyze(va, store, cfg.Analyze.KindSet(), vocab)
}
res, err := freshrssimport.Sync(ctx, fr, store, onIngested)
```

Same for `todoist.go` (wrapping with `todoistimport.OnIngestedFunc`).

- [ ] **Step 4: `cmd/darek/links.go`** — new command with two subcommands:

```go
func runLinks(ctx context.Context, cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: darek links <analyze|embed>")
	}
	switch args[0] {
	case "analyze":
		return runLinksAnalyze(ctx, cfgPath, args[1:])
	case "embed":
		return runLinksEmbed(ctx, cfgPath, args[1:]) // Task B3 implements the body
	default:
		return fmt.Errorf("unknown links subcommand %q (try: analyze, embed)", args[0])
	}
}
```

`runLinksAnalyze`:
- `fs := flag.NewFlagSet("links analyze", flag.ContinueOnError)`, flags `--limit int` (0 = all), `--kind string`, `--force bool`.
- Load config, open Postgres (shared helper `openPool(ctx, cfg)`), build analyzer (error if OpenAI unset), fetch vocab once.
- If `--force`, process all links (add `links.Store.Search` with a new `Analyzed *bool` filter, or a `PendingAnalysis` variant `AllForAnalysis`); else loop `PendingAnalysis(kind, batch)` until empty or limit reached.
- For each: `va.Analyze(ctx, analyze.Input{Title, URL, Body: l.Summary, KnownTags: vocab})` → `store.ApplyAnalysis`; count ok/err; print `links analyze: analyzed=%d errors=%d`.
- Tag `--force` support as optional; if it complicates the store, document `--force` as re-analyzing `analyzed_at IS NOT NULL` via a new `links.Store.RefreshAnalysis`.

> Keep this task's scope to the **non-force** path; make `--force` a follow-up if it needs an extra store method. Document that.

- [ ] **Step 5: Dispatch** — add `case "links": return runLinks(ctx, cfgPath, args)` and mention `links` in the unknown-subcommand message.

- [ ] **Step 6: Verify** — `go build ./... && go vet ./... && go test ./analyze/ ./links/ ./freshrssimport/ ./todoistimport/ ./config/`.
- [ ] **Step 7: Commit** — `feat(links): analyze new links on ingest + backfill command`.

---

# Part B — Recommendations

## Task B1 — Migration + `llm.Embed` (TDD)

**Files:** `db/migrations/0016_link_recommendations.up.sql`, `llm/client.go`, `llm/client_test.go`.

- [ ] **Step 1: Migration**

```sql
ALTER TABLE links ADD COLUMN embedded_at timestamptz;

CREATE INDEX links_embedding_hnsw ON links USING hnsw (embedding vector_cosine_ops);
CREATE INDEX links_analyzed_pending ON links (kind, created_at DESC) WHERE analyzed_at IS NULL;

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

- [ ] **Step 2: `llm.Embed`**

```go
// Embed returns one vector per input using the given embedding model. It reuses
// the same OpenAI client and records latency/count metrics.
func (cl *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, cl.timeout)
	defer cancel()

	start := time.Now()
	var resp *openai.CreateEmbeddingResponse
	err := func() error {
		ctx, span := tracer.Start(ctx, "llm.embed")
		defer span.End()
		span.SetAttributes(attribute.String("llm.model", model), attribute.Int("llm.embed.inputs", len(inputs)))
		r, e := cl.c.Embeddings.New(ctx, openai.EmbeddingNewParams{
			Model: model,
			Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: inputs},
		})
		if e != nil {
			return e
		}
		resp = r
		return nil
	}()
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	cl.m.LLMLatency.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(attribute.String("model", model), attribute.String("outcome", outcome), attribute.String("op", "embed")))
	if err != nil {
		return nil, fmt.Errorf("openai embed: %w", err)
	}
	out := make([][]float32, len(resp.Data))
	for _, d := range resp.Data {
		v := make([]float32, len(d.Embedding))
		for i, f := range d.Embedding {
			v[i] = float32(f)
		}
		out[d.Index] = v
	}
	return out, nil
}
```

> `llm` currently has no `tracer` var; add `var tracer = otel.Tracer("darek/llm")` (import `go.opentelemetry.io/otel`).

- [ ] **Step 3: Test** — using `httptest.NewServer` to stub `POST /embeddings` and asserting the parsed vectors + that an API-key-only client reaches it (`llm.Options{APIKey:"x", BaseURL: srv.URL, Model: "gpt-4.1"}`). Assert empty input → nil, nil.

- [ ] **Step 3b: Migration test** — add to any integration suite (or `db/`): `require.NoError(t, db.Migrate(...))` then assert `link_recommendations` exists (covered implicitly by B2).

- [ ] **Step 4: Verify** — `go build ./... && go test ./llm/`.
- [ ] **Step 5: Commit** — `feat(llm): embeddings support + link_recommendations migration`.

---

## Task B2 — `recommend` store (TDD, integration)

**Files:** `recommend/store.go`, `recommend/store_integration_test.go`.

- [ ] **Step 1: Types + methods**

```go
package recommend

type EmbedText struct { ID uuid.UUID; Text string }
type Example struct {
	ID uuid.UUID
	Title, Kind, Feed, Summary string
	Rating int
	Embedding []float32
}
type Candidate struct {
	ID uuid.UUID
	URL, Title, Kind, Feed, Summary string
	Score float32
}
type Suggestion struct {
	Candidate
	Reason string
}

type Store struct{ pool *db.Pool }
func NewStore(pool *db.Pool) *Store { return &Store{pool: pool} }
```

Text expression (shared const):
```go
const embeddingTextExpr = `coalesce(title,'') || ' ' || coalesce(summary,'') || ' ' ||
	immutable_array_to_string(tags, ' ') || ' ' || coalesce(feed,'')`
```

Methods:
- `PendingEmbeddings(ctx, limit) ([]EmbedText, error)` — `SELECT id, <expr> FROM links WHERE embedded_at IS NULL AND (<expr>) <> '' ORDER BY created_at DESC LIMIT $1`.
- `SetEmbedding(ctx, id, vec []float32) error` — pass `vectorString(vec)` as `$2` and `CAST($2 AS vector)`; `SET embedding = CAST($2 AS vector), embedded_at = now()`.
- `RatedExamples(ctx, minRating, maxRating, limit) ([]Example, error)` — `WHERE rating >= $min AND rating <= $max AND embedding IS NOT NULL` scanning id,title,kind,feed,summary,rating,embedding (embedding scanned as `[]float32` via a `scanVector([]byte)` helper parsing `[a,b,c]`).
- `VectorCandidates(ctx, liked, disliked []float32, lookbackDays, cooldownDays, perKind) ([]Candidate, error)` — the SQL in §7.2 of the spec, once per `kind in (article, video)`, interleaved.
- `TextCandidates(ctx, query string, lookbackDays, cooldownDays, perKind) ([]Candidate, error)` — same filters but `search @@ plainto_tsquery('simple', $q)` and `ORDER BY ts_rank(...) DESC`.
- `SaveRecommendations(ctx, picks []Suggestion, source string) error` — `INSERT ... ON CONFLICT (link_id) DO NOTHING` in a tx.

Helpers:
```go
func vectorString(v []float32) string        // "[0.1,0.2,...]"
func scanVector(row pgx.Row) ([]float32, error) // for rated examples
func interleave(a, b []Candidate, max int) []Candidate
```

- [ ] **Step 3: Integration tests** (`//go:build integration`, package `recommend`, helper `newRecStore(t)` mirroring `links`)

  - `TestPendingEmbeddings` — new links appear; `SetEmbedding` removes them.
  - `TestRatedExamples` — only `>=4`/`<=2` returned; requires non-null embedding (set via `SetEmbedding`).
  - `TestVectorCandidates_ExcludesRatingSeenAndOld` — seed rated pairs + unrated candidates with hand-made embeddings; assert unrated+embedded+in-window+not-recommended only, correctly ordered by the score expression.
  - `TestVectorCandidates_KindBalanced` — seed 5 articles/5 videos; `perKind=2` → ⌊2 articles + 2 videos⌋ interleaved.
  - `TestSaveRecommendations_Dedup` — second insert of same link is a no-op (count stays 1); subsequent candidate query excludes it.
  - `TestTextCandidates` — tsquery match on title/summary.

  Build embeddings by hand, e.g. `[]float32{1,0,0,...}` padded to 1536 (`vec := make([]float32,1536); vec[0]=1`).

- [ ] **Step 4: Run** — `go test -tags integration ./recommend/ -run TestPendingEmbeddings -v` etc.
- [ ] **Step 5: Commit** — `feat(recommend): store (embeddings, profile, candidates, persistence)`.

---

## Task B2a — `recommend/embed.go` (TDD, unit + integration)

**Files:** `recommend/embed.go`, `recommend/embed_test.go`.

- [ ] **Step 1: Implement**

```go
type Embedder interface {
	Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}

// EnsureEmbeddings embeds up to `limit` pending links in batches of batchSize.
// Returns the number embedded.
func EnsureEmbeddings(ctx context.Context, store *Store, e Embedder, model string, limit, batchSize int) (int, error) {
	if limit <= 0 { limit = 256 }
	if batchSize <= 0 { batchSize = 64 }
	done := 0
	for total := 0; limit == 0 || total < limit; {
		n := min(batchSize, limit-amount) // clamp
		pending, err := store.PendingEmbeddings(ctx, n)
		if err != nil { return done, err }
		if len(pending) == 0 {
			break
		}
		texts := make([]string, len(pending))
		for i, p := range pending {
			texts[i] = p.Text
		}
		vecs, err := e.Embed(ctx, model, texts)
		if err != nil {
			return done, fmt.Errorf("embed batch: %w", err)
		}
		for i, vec := range vecs {
			if err := store.SetEmbedding(ctx, pending[i].ID, vec); err != nil {
				return done, err
			}
			done++
		}
		if len(pending) < batchSize {
			break
		}
	}
	return done, nil
}
```

- [ ] **Step 4: Tests**
  - Unit: fake `Embedder` returning deterministic vectors; assert batching and that `SetEmbedding` is called per item (use the integration store or a fake `embedSink` interface — prefer an interface `embedStore` for unit testing).
  - Integration: real store, fake embedder, assert `embedded_at` set and `PendingEmbeddings` drains.

- [ ] **Step 5: Commit** — `feat(recommend): embedding backfill`.

---

## Task B2b — `darek links embed` command

**Files:** `cmd/darek/links.go`.

- [ ] Wire `runLinksEmbed`: flag `--limit int` (default 0 = all), load config, open pool, `llm.New` + `recommend.NewStore`, call `EnsureEmbeddings(ctx, store, llmClient, cfg.Recommend.EmbeddingModel, limit, cfg.Recommend.EmbedBatch)`, print `links embed: embedded=%d`.
- [ ] Default `EmbeddingModel` to `text-embedding-3-small` when empty (helper on the config struct).
- [ ] Verify build; commit — `feat(links): embed backfill command`.

---

## Task B3 — Profile + candidates (TDD, integration)

**Files:** `recommend/profile.go`, `recommend/candidates.go` (or fold into profile.go), `recommend/candidates_integration_test.go`.

- [ ] **Step 1: Implement**

```go
type Profile struct {
	Liked, Disliked     []Example
	LikedCentroid       []float32
	DislikedCentroid    []float32
}

func BuildProfile(ctx context.Context, store *Store) (Profile, error) {
	liked, err := store.RatedExamples(ctx, 4, 5, 50)
	if err != nil {
		return Profile{}, err
	}
	disliked, err := store.RatedExamples(ctx, 1, 2, 50)
	if err != nil {
		return Profile{}, err
	}
	return Profile{
		Liked: liked, Disliked: disliked,
		LikedCentroid:    centroid(embeddings(liked)),
		DislikedCentroid: centroid(embeddings(disliked)),
	}, nil
}

func centroid(vecs [][]float32) []float32 // nil if empty; mean then L2-normalize
```

Candidate fetch:
```go
func Candidates(ctx, store *Store, p Profile, opt Options) ([]Candidate, bool, error)
// vector path when p.LikedCentroid != nil && len(p.Liked) >= opt.MinLiked
//   → store.VectorCandidates(liked, dislikedOrZero, opt.LookbackDays, opt.CooldownDays, perKind)
// text fallback otherwise
//   → query = strings.Join(topN(likedTitles+tags), " "); store.TextCandidates(...)
// returns (candidates, usedVector, err)
```

- [ ] **Step 2: Tests** — `TestCentroid` (unit: mean + normalized; empty → nil), `TestCandidates_FallbackToText` (integration: no embeddings → text path), `TestCandidates_VectorBalanced` (integration).
- [ ] **Step 3: Commit** — `feat(recommend): taste profile + candidate retrieval`.

---

## Task B4 — LLM selector (TDD, unit)

**Files:** `recommend/select.go`, `recommend/select_test.go`.

- [ ] **Step 1: Implement** (mirror `memory/extract.go`)

```go
type Chat interface {
	Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}
type Selector struct{ llm Chat }
func NewSelector(c Chat) *Selector { return &Selector{llm: c} }

func (s *Selector) Select(ctx context.Context, p Profile, cands []Candidate, count int) ([]Suggestion, error)
```

System prompt:

```
You are Darek's reading recommender. The user is Bartek. Given a taste profile
(links he liked and disliked) and a numbered list of unrated candidates, choose
up to N links he is most likely to enjoy reading. Reply with strict JSON:
{"recommendations": [{"index": <int>, "reason": "<one short sentence>"}]}
Rules: index refers to the candidate list; never invent links; include at least
one article and one video when reasonable candidates of both kinds exist; keep
reasons concrete and under 20 words; if nothing is a good fit, return an empty list.
```

User message: taste profile (`Liked:` bullets `- [kind] title — summary(≤160)`, `Disliked:` same) then `Candidates:` bullets `[i] (kind, feed) title — summary(≤160)`.

`ResponseFormat: OfJSONObject`. Parse via `parsePicks(content, len(cands), count)`:
- strip code fence;
- unmarshal `{"recommendations":[{"index":int,"reason":string}]}`;
- drop out-of-range/duplicate indexes; cap at `count`; attach `cands[index]`.

- [ ] **Step 2: Tests** — `TestSelect_HappyPath` (fake Chat), `TestSelect_FencedJSON`, `TestParsePicks_DropsOutOfRangeAndDupes`, `TestSelect_EmptyCandidates` (returns nil, no call), `TestSelect_InvalidJSON` (error).
- [ ] **Step 3: Commit** — `feat(recommend): LLM selector`.

---

## Task B5 — Renderers (TDD, unit)

**Files:** `recommend/render.go`, `recommend/render_test.go`.

- [ ] `RenderText(rs []Suggestion) string` / `RenderHTML(rs []Suggestion) string`, empty → `""`, with `htmlEscape`. Text:

```
Reading suggestions
───────────────────

▸ [article] Title — feed
  Why: reason
  https://url
```

- HTML: a `<section>` with `<h2>Reading suggestions</h2>`, one inline-styled card per suggestion (title link, kind/feed, reason).
- Tests: `TestRenderText` (contains title/kind/reason/url; kind-aware), `TestRenderText_Empty`, `TestRenderHTML_Escapes` (`<script>` escaped).
- Commit — `feat(recommend): digest renderers`.

---

## Task B5a — Config `Recommend` + metrics

**Files:** `config/types.go`, `obs/metrics.go`, `obs/metrics_test.go`.

- [ ] Config struct + defaults helpers:

```go
type Recommend struct {
	Enabled        bool   `yaml:"enabled"`
	Count          int    `yaml:"count"`
	CandidatePool  int    `yaml:"candidate_pool"`
	LookbackDays   int    `yaml:"lookback_days"`
	CooldownDays   int    `yaml:"cooldown_days"`
	MinLiked       int    `yaml:"min_liked"`
	EmbeddingModel string `yaml:"embedding_model"`
	EmbedBatch     int    `yaml:"embed_batch"`
	EmbedLimit     int    `yaml:"embed_limit"`
}
func (r Recommend) WithDefaults() Recommend // count=3, pool=30, min_liked=3, model=text-embedding-3-small, batch=64, limit=256
```

- [ ] Metrics: `RecommendRuns`, `RecommendSuggestions` (label `source`), `RecommendEmbeddings`.
- [ ] Tests: assert the counters initialize (extend `obs/metrics_test.go`).
- [ ] Commit — `feat(recommend): config + metrics`.

---

## Task B5c — Orchestrator `recommend.Build` (TDD, integration)

**Files:** `recommend/recommend.go`, `recommend/recommend_integration_test.go`.

- [ ] **Step 1: Implement**

```go
type Options struct {
	Count, CandidatePool, LookbackDays, CooldownDays, MinLiked int
	EmbeddingModel string
	EmbedLimit, EmbedBatch int
}
type Result struct {
	Suggestions []Suggestion
	Embedded    int
	Candidates  int
	Vector      bool
}
func Build(ctx context.Context, store *Store, emb Embedder, sel *Selector, opt Options, source string, logf func(string, ...any)) (Result, error)
```

Steps:
1. `var res Result`
2. if `emb != nil`: `res.Embedded, _ = EnsureEmbeddings(...)` (log on error, continue).
3. `p, err := BuildProfile(ctx, store)` → return err on DB failure.
4. `cands, usedVector, err := Candidates(ctx, store, p, opt)` → return err.
5. `res.Candidates = len(cands); res.Vector = usedVector`; if empty → return res.
6. `picks, err := sel.Select(ctx, p, cands, opt.Count)`; on error/empty log and fall back to `topN(cands, opt.Count)` with reason `""`.
7. `store.SaveRecommendations(ctx, picks, source)` (log on error).
8. `res.Suggestions = picks`; return.

- [ ] **Step 2: Tests** (integration, package `recommend`, fake `Chat` + fake `Embedder`): suggestions returned and persisted; second `Build` returns fewer (already-recommended excluded); LLM error → fallback top-N; no rated/embeddings → text fallback doesn't error.
- [ ] **Step 3: Commit** — `feat(recommend): orchestrator`.

---

## Task B6 — Digest wiring

**Files:** `cmd/darek/daily_digest.go`.

- [ ] **Step 1:** Change the pool-open condition to `if cfg.WhatsApp.Enabled || cfg.Recommend.Enabled`.
- [ ] **Step 2:** After the memory block, add:

```go
var recText, recHTML string
if cfg.Recommend.Enabled && pool != nil {
	apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
	if err != nil || apiKey == "" {
		fmt.Fprintf(os.Stderr, "info: openai not configured, skipping recommendations\n")
	} else if llmClient, err := llm.New(llm.Options{APIKey: apiKey, BaseURL: cfg.OpenAI.BaseURL, Model: cfg.OpenAI.Model, Timeout: cfg.Agent.LLMTimeout}); err != nil {
		fmt.Fprintf(os.Stderr, "warn: llm client for recommendations: %v\n", err)
	} else {
		ro := cfg.Recommend.WithDefaults()
		recStore := recommend.NewStore(pool)
		res, err := recommend.Build(ctx, recStore, llmClient, recommend.NewSelector(llmClient), recommend.Options{
			Count: opt.Count, CandidatePool: ..., LookbackDays: ..., CooldownDays: ..., MinLiked: ...,
			EmbeddingModel: rec.EmbeddingModel, EmbedLimit: res..., EmbedBatch: ...,
		}, "digest", logf)
		if err != nil { fmt.Fprintf(os.Stderr, "warn: recommendations failed: %v\n", err) }
		recText = recommend.RenderText(res.Suggestions)
		recHTML = recommend.RenderHTML(res.Suggestions)
		fmt.Fprintf(os.Stderr, "info: recommend: candidates=%d vector=%t embedded=%d suggestions=%d\n",
			res.Candidates, res.Vector, res.Embedded, len(res.Suggestions))
	}
}
```

- [ ] **Step 3:** Compose order: calendar → recommendations → memory → whatsapp:

```go
if recText != "" { text += "\n\n" + recText; html += recHTML }
if memText != "" { text += "\n\n" + memText; html += memHTML }
if waText  != "" { text += "\n\n" + waText;  html += waHTML }
```

- [ ] **Step 4:** Verify `go build ./... && go vet ./...`. Commit — `feat(digest): reading-suggestions section`.

---

## Task B7 — `links.recommend` chat tool

**Files:** `recommend/tool.go`, `recommend/tool_integration_test.go`, `cmd/darek/chat.go`.

- [ ] **Step 1:** Implement `Tool` with `Name() "links.recommend"`, schema (`count` 1–10 default 3, `kind` optional, `include_seen` bool default false), and `Execute` calling `Build` with `source:"chat"`. When `include_seen` is false, pass `CooldownDays=0` (never-repeat); when true, still fetch candidates but allow already-seen by using a large negative lookback… **simpler:** add `Options.IncludeSeen bool` to `Candidates`/`Build` that skips the `NOT EXISTS(link_recommendations)` clause. Implement that flag in `store.VectorCandidates`/`TextCandidates` (pass `cooldownDays = -1` to mean "ignore seen").
- [ ] **Step 2:** Register in `chat.go` next to the links tools:

```go
if err := reg.Register(recommend.NewTool(recommendStore, llmClient, cfg.Recommend.WithDefaults())); err != nil {
	return err
}
```

(Construct `recommendStore := recommend.NewStore(pool)` and reuse the existing `llmClient`; if OpenAI is unconfigured, `runChat` already handles that path — guard registration accordingly.)

- [ ] **Step 3:** Integration test: seed rated+unrated, fake chat, assert tool output contains a suggested title; `include_seen` includes previously-seen.
- [ ] **Step 4:** Commit — `feat(recommend): links.recommend chat tool`.

---

## Task B8 — README + final verification

- [ ] Document Part A (`analyze.enabled`, `darek links analyze`) and Part B (`recommend.*`, digest section, `links.recommend`, `darek links embed`) in `README.md`, cross-referencing the existing Links and Daily-digest sections.
- [ ] Run:
  - `go build ./...`
  - `go vet ./...`
  - `make test`
  - `make lint`
  - `TESTCONTAINERS_RYUK_DISABLED=true go test -tags=integration -count=1 ./...`
  - `CGO_ENABLED=0 go build -o /tmp/darek-test ./cmd/darek`
- [ ] Commit — `docs(links): analysis + recommendations`.

---

## Deployment (after implementation)

1. Bump the image tag and set in `homelab-k8s/values/darek.yaml`:
   ```yaml
   analyze:
     enabled: true
     kinds: [article, video]
   recommend:
     enabled: true
   ```
2. Run one-time backfills (as a Job from the new image): `darek links analyze --limit 600` then `darek links embed`.
3. Trigger a digest to confirm the "Reading suggestions" section renders.
