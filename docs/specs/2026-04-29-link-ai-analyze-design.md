# Darek — AI summarize + tag-propose for links (design)

**Date:** 2026-04-29
**Status:** approved (awaiting implementation plan)
**Author:** brainstormed with Claude

## 1. Goal

Add an "Analyze" button to every link in the inbox UI. Clicking it sends the link's title + URL + source-provided body to OpenAI, gets back a short summary and 3–7 proposed tags, and stores both on the link. The summary is a single text field that may also be populated by the ingestion source (e.g., RSS feed body); AI overwrites it on demand. Proposed tags merge into the link's existing tags array.

## 2. Scope

### In

- New `summary` and `analyzed_at` columns on `links`.
- `links.Candidate.Summary` field so any ingestion source can pre-populate the summary at ingest time. RSS sets it from `Article.Summary` (HTML-stripped). Future Todoist source will set it from task description; design assumes it.
- New `analyze/` package with a single-method `Analyzer.Analyze(ctx, Input) (Output, error)`. Wraps `llm.Client.Chat` behind a tiny `Chat` interface so it's testable with a fake.
- `POST /links/{id}/analyze` handler that calls the analyzer, persists result (overwrites summary, merges tags, sets `analyzed_at`), and returns the swapped row partial.
- "Analyze" button per row, label flips to "Re-analyze" when `analyzed_at` is set. Loading state via `hx-indicator` and CSS only — no custom JS.
- One new metric instrument: `darek.links.analyze` counter (labels: `outcome`). Token/cost flow through existing LLM instruments.

### Out (deferred)

- Batch analyze-all. Single-row only.
- Async job queue. Handler blocks the HTTP request for the LLM call.
- Fetching article URL ourselves to get fuller text. Source-provided body only for v1.
- Per-prompt customization in the UI. Prompt lives in code.
- Search-index integration of `summary` (the tsvector generated column does not include it). Can opt in later.

## 3. Schema

Migration `db/migrations/0005_links_summary.up.sql`:

```sql
ALTER TABLE links ADD COLUMN summary     text;
ALTER TABLE links ADD COLUMN analyzed_at timestamptz;
```

- `summary` — single text field. Populated either by the source (HTML-stripped) at ingest, or by AI on demand. AI overwrites if already present.
- `analyzed_at` — non-null = AI has run on this link. Drives the button label.
- No new index — these columns are payload, not filter targets.
- The existing tsvector `search` generated column stays as-is (title + notes + tags + url). Including summary is a follow-up.

## 4. `links` package changes

### `Link` struct

```go
type Link struct {
    ID         uuid.UUID
    URL        string
    Title      string
    Rating     *int
    Tags       []string
    Notes      string
    Source     string
    Kind       string
    Feed       string
    Summary    string
    AnalyzedAt *time.Time // nil = AI hasn't run yet
    CreatedAt  time.Time
    UpdatedAt  time.Time
}
```

### `Candidate` struct

```go
type Candidate struct {
    URL     string
    Title   string
    Source  string
    Feed    string
    Kind    string
    Summary string // optional source-provided summary
}
```

### `Save` and `SaveInput`

`SaveInput` gains `Summary string` (existing "leave alone if empty" semantics on update).

INSERT path adds `summary` to the column list and value list (uses `NULLIF($N, '')` so empty stays NULL). UPDATE path adds a conditional `summary = $N` to the set-list mirroring the existing `kind`/`feed` pattern.

### `Search` / `Similar` / `Get`

SELECT lists gain `coalesce(summary,'')` and `analyzed_at`. `scanLinks` and `Similar`'s inline scan add the matching scan targets.

### `IngestOne`

After canonicalization + classification, strips HTML from `c.Summary` (via `links.StripHTML` — a small new utility, ~15 lines) before saving. The current `Save` call gains the `Summary` field passthrough.

### `links.StripHTML(html string) string`

Plain text from a possibly-HTML body. Implementation: parse with `golang.org/x/net/html` (already in go.mod transitively via google API client) and concatenate text nodes with whitespace. Trim collapsed whitespace runs.

If the html package isn't already a direct dep, the simpler approach is `regexp.MustCompile("<[^>]+>")` — good enough for RSS summaries which are usually well-formed paragraph HTML. Pick whichever is simpler when implementing.

## 5. `freshrssimport.Sync` change

One line: when building `Candidate`, populate `Summary: art.Summary`. Existing tests still pass because the field is optional.

## 6. `analyze` package

New package `analyze/` (peer of `links/`, `freshrssimport/`).

### Public API

```go
package analyze

import (
    "context"

    "github.com/openai/openai-go"
)

type Input struct {
    Title string
    URL   string
    Body  string // plain text; may be empty
}

type Output struct {
    Summary string
    Tags    []string
}

// Chat is the subset of *llm.Client used by Analyzer.
type Chat interface {
    Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

type Analyzer struct {
    llm Chat
}

func New(c Chat) *Analyzer { return &Analyzer{llm: c} }

func (a *Analyzer) Analyze(ctx context.Context, in Input) (Output, error)
```

### Prompt

System prompt (constant in `analyze/analyze.go`):

> You are summarizing links the user is considering reading. Reply with strict JSON only:
> `{"summary": "...", "tags": ["...", "..."]}`
> The summary is 1–3 plain-text sentences, factual, no marketing language. Tags are 3–7 lowercase short topical labels (single word or hyphenated bigram). Do not invent facts. If the body is empty or unrelated to the title, summarize from the title alone.

User message: `Title: <title>\nURL: <url>\nBody:\n<body>` where body is truncated to 6000 characters.

### Output processing

- `json.Unmarshal` the model response.
- Tags: lowercased, trimmed, deduped, blanks dropped, capped at 7.
- Malformed JSON → return error to caller. No silent fallback.

### Test plan

`analyze/analyze_test.go` with a fake `Chat`:

- Happy path: returns expected summary + tags.
- Tag normalization: mixed case + dupes + blanks → lowercase, deduped, ≤7.
- Malformed model response → error.
- Empty body: prompt still goes out; returns sensible result based on title.

### Observability

The analyzer makes no metric calls of its own. `*llm.Client.Chat` is already wrapped by `obs.Dep("openai_chat","chat", ...)` and records token / cost / latency through `darek.tokens.*`, `darek.llm.cost_usd`, `darek.llm.latency`, and `darek.dep.*`.

## 7. HTTP server changes

### Route

`POST /links/{id}/analyze` — calls the analyzer, persists, returns the swapped row partial.

### `Server` struct

```go
type Server struct {
    store   *links.Store
    tmpl    *template.Template
    mux     *http.ServeMux
    sync    SyncFn
    analyze *analyze.Analyzer
}

func New(store *links.Store, sync SyncFn, analyzer *analyze.Analyzer) (*Server, error)
```

If `analyzer` is nil, the route returns 501 and the button is hidden in templates.

### Handler

```go
func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
    id, err := uuid.Parse(r.PathValue("id"))
    if err != nil { /* 400 */ }
    if s.analyze == nil { /* 501 */ }
    cur, err := s.fetchOne(r.Context(), id)
    if err != nil { /* 500 */ }

    out, err := s.analyze.Analyze(r.Context(), analyze.Input{
        Title: cur.Title, URL: cur.URL, Body: cur.Summary,
    })
    if err != nil {
        if m, _ := obs.MetricsInstance(); m != nil {
            m.LinksAnalyze.Add(r.Context(), 1, metric.WithAttributes(
                attribute.String("outcome", "error")))
        }
        // render error inline in the row
        cur.Summary = fmt.Sprintf("analysis failed: %v", err)
        s.tmpl.ExecuteTemplate(w, "_row.html", toLinkVM(cur))
        return
    }

    _, err = s.store.Pool().Exec(r.Context(), `
        UPDATE links
           SET summary     = $2,
               tags        = ARRAY(SELECT DISTINCT unnest(tags || $3::text[])),
               analyzed_at = now(),
               updated_at  = now()
         WHERE id = $1`, id, out.Summary, out.Tags)
    if err != nil { /* 500 */ }

    if m, _ := obs.MetricsInstance(); m != nil {
        m.LinksAnalyze.Add(r.Context(), 1, metric.WithAttributes(
            attribute.String("outcome", "ok")))
    }
    cur, err = s.fetchOne(r.Context(), id)
    if err != nil { /* 500 */ }
    s.tmpl.ExecuteTemplate(w, "_row.html", toLinkVM(cur))
}
```

The whole row is swapped, not a fragment — three things change at once (summary block, tags, button label).

### Template changes

`_row.html` gains a summary block (when `.Summary` non-empty) and an analyze button at the end of the meta line. The button is only rendered when the view-model says analysis is enabled.

```html
{{if .Summary}}
  <div class="summary">{{.Summary}}</div>
{{end}}
```

```html
{{if .AnalyzeEnabled}}
  <button class="analyze" hx-post="/links/{{.ID}}/analyze" hx-target="#row-{{.ID}}" hx-swap="outerHTML" hx-indicator="#row-{{.ID}} .analyze">
    {{if .AnalyzedAt}}Re-analyze{{else}}Analyze{{end}}
  </button>
{{end}}
```

`linkVM` gains:

```go
type linkVM struct {
    // …existing fields…
    Summary        string
    AnalyzedAt     *time.Time
    AnalyzeEnabled bool
}
```

`AnalyzeEnabled` is set per-row from a server-level flag passed into `toLinkVM` (or a method on `*Server`). Cleanest: thread it through as a parameter to `toLinkVM(l, analyzeEnabled)`.

### CSS

Add to `style.css` (~15 lines):

- `.summary` — small italic dimmed text block, slight left border or indent.
- `.analyze` — small button matching meta line, with `htmx-request` state showing "thinking…".

### Wiring in `cmd/darek/serve.go`

The existing `runServe` builds the LLM client only inside the FreshRSS branch. Lift it up: build `llm.Client` unconditionally (it only needs the OpenAI key, not FreshRSS). Pass the analyzer into `serve.New`.

```go
llmClient, err := llm.New(llm.Options{
    APIKey:  apiKey,
    BaseURL: cfg.OpenAI.BaseURL,
    Model:   cfg.OpenAI.Model,
    Timeout: cfg.Agent.LLMTimeout,
})
if err != nil { /* warn + nil analyzer */ }
analyzer := analyze.New(llmClient)
srv, err := serve.New(store, sync, analyzer)
```

If the OpenAI key isn't configured, `llmClient` construction fails — log to stderr and pass a nil analyzer to the server. Button stays hidden.

## 8. Observability

New instrument in `obs/metrics.go`:

- `LinksAnalyze` — `metric.Int64Counter`, OTel name `darek.links.analyze`, labels: `outcome` ∈ {`ok`, `error`}.

Cardinality test gains the new outcome enum (already covered by existing `outcome` allowlist; nothing to change).

Existing instruments cover the rest:
- `darek.tokens.*`, `darek.llm.cost_usd`, `darek.llm.latency` — bumped by `llm.Client.Chat`.
- `darek.dep.requests` / `darek.dep.latency` with `dep="openai_chat"` — also from `llm.Client.Chat`.

## 9. Testing

- `analyze/analyze_test.go` — fake `Chat`, four cases above.
- `links/canonicalize_test.go` / `classify_test.go` unchanged.
- `links/ingest_test.go` extended: a `Candidate.Summary` is HTML-stripped and stored.
- HTTP handler — smoke test that `POST /links/{id}/analyze` returns 200 when the analyzer is configured (with a fake analyzer wired into `Server`).
- Manual verification: `darek serve`, click Analyze on an item, summary + tags appear; click again to re-analyze.

## 10. Risks / migration

- `0005` migration is additive; existing rows get `summary=NULL`, `analyzed_at=NULL`. Safe.
- Existing list views work unchanged because `summary` is shown only when non-empty.
- Token/cost: bounded by the 6000-char input cap. Worst case ~1500 input + ~300 output tokens per analyze. With current model pricing this is well under $0.01 per click.
- The handler blocks for 2–5s on the OpenAI call. HTMX shows the loading state. If users find this annoying, a job queue is a separate plan.
- `Body` is plain-text after HTML strip. RSS feeds with code blocks / preformatted text may render as unstructured text in the prompt; acceptable.

## 11. Out of scope

Recapped from §2:

- Batch analyze-all.
- Async job queue.
- Fetching article URL ourselves.
- Prompt customization in the UI.
- Search index includes summary.
