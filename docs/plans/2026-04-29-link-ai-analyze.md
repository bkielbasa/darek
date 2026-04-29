# AI summary + tag-propose for links — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an "Analyze" button to every link in the inbox UI that sends title + URL + source-provided body to OpenAI, gets back a short summary and 3–7 tags, and stores them on the link (summary in a new column, tags merged into the existing array).

**Architecture:** A new `analyze/` package wraps `llm.Client.Chat` behind a tiny `Chat` interface; `POST /links/{id}/analyze` is the one user-facing trigger. The `links.Candidate` ingestion struct gains a `Summary` field so any source (RSS today, Todoist tomorrow) can pre-populate the summary at ingest time, with HTML stripped before storage.

**Tech Stack:** Go 1.22+, `github.com/openai/openai-go` (already a dep), `html/template` + HTMX (already wired), pgx/v5.

**Spec:** [`docs/specs/2026-04-29-link-ai-analyze-design.md`](../specs/2026-04-29-link-ai-analyze-design.md)

**Out of scope:** batch analyze-all, async job queue, fetching the article URL ourselves, prompt UI customization, search-index over `summary`.

---

## File Map

| Path | Responsibility |
|---|---|
| `db/migrations/0005_links_summary.up.sql` | `summary` + `analyzed_at` columns. |
| `links/store.go` | `Link` + `SaveInput` extension, INSERT/UPDATE/SELECT updates, scan sites. |
| `links/striphtml.go` | `StripHTML(s string) string` — plain text from RSS HTML body. |
| `links/striphtml_test.go` | Fixture-table unit test. |
| `links/ingest.go` | `Candidate.Summary` field; `IngestOne` strips and passes through. |
| `links/ingest_test.go` | Extended: HTML-strip + summary persisted. |
| `freshrssimport/sync.go` | Pass `art.Summary` into `Candidate`. |
| `analyze/analyze.go` | `Chat` interface, `Analyzer`, `Analyze` method, prompt. |
| `analyze/analyze_test.go` | Fake-Chat unit tests. |
| `obs/metrics.go` | Add `LinksAnalyze` counter. |
| `obs/metrics_test.go` | Extend nil-checks. |
| `cmd/darek/serve/server.go` | `Server.analyze` field; `New` signature gains `*analyze.Analyzer`. |
| `cmd/darek/serve/handlers.go` | `handleAnalyze`; `linkVM` gains `Summary`/`AnalyzedAt`/`AnalyzeEnabled`; `toLinkVM` parameterizes the flag. |
| `cmd/darek/serve/templates/_row.html` | Summary block + Analyze button (conditional). |
| `cmd/darek/serve/static/style.css` | `.summary` and `.analyze` (incl. `htmx-request` loading state). |
| `cmd/darek/serve/server_test.go` | Update `serve.New(...)` calls to three-arg signature. |
| `cmd/darek/serve.go` | Build LLM client unconditionally; pass analyzer into `serve.New`. |

---

## Conventions

- Tasks 1–11 are code; Task 12 is manual verification.
- TDD where practical: pure helpers (`StripHTML`, `analyze.Analyze`) are test-first; integration tasks add to existing test files.
- Frequent commits — one per task. Build green at each step (`go build ./...`, `go vet ./...`, `go test ./...`).
- Counter bumps and metric reads are guarded by `if m != nil` per the project's "instrumentation never blocks real work" pattern.

---

## Task 1 — Migration: `summary` + `analyzed_at`

**Files:**
- Create: `db/migrations/0005_links_summary.up.sql`

- [ ] **Step 1: Create the file**

```sql
ALTER TABLE links ADD COLUMN summary     text;
ALTER TABLE links ADD COLUMN analyzed_at timestamptz;
```

- [ ] **Step 2: Run migration tests**

```
go test -tags=integration -count=1 ./db/...
```

Expected: PASS — the existing `TestMigrate_*` tests run all up-migrations and confirm idempotency.

- [ ] **Step 3: Commit**

```
git add db/migrations/0005_links_summary.up.sql
git commit -m "feat(db): links.summary + links.analyzed_at for AI analyze"
```

---

## Task 2 — `links.Link`, `SaveInput`, `Save`, scan sites

**Files:**
- Modify: `links/store.go`

- [ ] **Step 1: Extend the `Link` struct**

Replace the `Link` struct with:

```go
type Link struct {
	ID         uuid.UUID
	URL        string
	Title      string
	Rating     *int // nil = unrated
	Tags       []string
	Notes      string
	Source     string
	Kind       string
	Feed       string
	Summary    string
	AnalyzedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
```

- [ ] **Step 2: Extend `SaveInput`**

```go
type SaveInput struct {
	URL         string
	Title       string
	Rating      *int
	Tags        []string
	Notes       string
	Source      string // defaults to "user"
	Kind        string
	Feed        string
	Summary     string // optional source-provided summary; empty leaves existing intact on update
	ReplaceTags bool
}
```

- [ ] **Step 3: Update `Save` — INSERT branch**

Locate the INSERT branch (`case errors.Is(err, pgx.ErrNoRows):`). Replace with:

```go
	case errors.Is(err, pgx.ErrNoRows):
		// Insert
		kind := in.Kind
		if kind == "" {
			kind = "article"
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO links (url, title, rating, tags, notes, source, kind, feed, summary)
			VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''))
			RETURNING id
		`, in.URL, in.Title, in.Rating, in.Tags, in.Notes, in.Source, kind, in.Feed, in.Summary).Scan(&id)
		if err != nil {
			return uuid.Nil, fmt.Errorf("insert: %w", err)
		}
		op = "save_new"
```

- [ ] **Step 4: Update `Save` — UPDATE branch**

Find the conditional set-list (where `kind` and `feed` are appended). Right after those, append summary:

```go
		if in.Summary != "" {
			args = append(args, in.Summary)
			set = append(set, fmt.Sprintf("summary = $%d", len(args)))
		}
```

- [ ] **Step 5: Update SELECT lists**

`Search` SQL — find the SELECT clause that lists `id, url, ..., kind, coalesce(feed,''), created_at, updated_at`. Replace with:

```go
q := `
	SELECT id, url, coalesce(title,''), rating, tags, coalesce(notes,''), source, kind, coalesce(feed,''), coalesce(summary,''), analyzed_at, created_at, updated_at
	FROM links
	WHERE ` + strings.Join(conds, " AND ") + `
	ORDER BY ` + orderBy(o) + `
	LIMIT $` + fmt.Sprint(len(args))
```

`Similar` SQL — same insertion of `coalesce(summary,''), analyzed_at` between `coalesce(feed,'')` and `created_at`:

```go
	rows, err := s.pool.Query(ctx, `
		SELECT id, url, coalesce(title,''), rating, tags, coalesce(notes,''), source, kind, coalesce(feed,''), coalesce(summary,''), analyzed_at, created_at, updated_at,
		       ts_rank(search, plainto_tsquery('simple', $1)) AS rank
		FROM links
		WHERE rating IS NOT NULL
		  AND search @@ plainto_tsquery('simple', $1)
		ORDER BY rank DESC, rating DESC NULLS LAST, created_at DESC
		LIMIT $2
	`, text, limit)
```

`Get` SQL — same insertion:

```go
	rows, err := s.pool.Query(ctx, `
		SELECT id, url, coalesce(title,''), rating, tags, coalesce(notes,''), source, kind, coalesce(feed,''), coalesce(summary,''), analyzed_at, created_at, updated_at
		FROM links WHERE id = $1
	`, id)
```

- [ ] **Step 6: Update scan sites**

`scanLinks`:

```go
func scanLinks(rows pgx.Rows) ([]Link, error) {
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.URL, &l.Title, &l.Rating, &l.Tags, &l.Notes, &l.Source, &l.Kind, &l.Feed, &l.Summary, &l.AnalyzedAt, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
```

`Similar`'s inline scan body:

```go
		var l Link
		var rank float32
		if err := rows.Scan(&l.ID, &l.URL, &l.Title, &l.Rating, &l.Tags, &l.Notes, &l.Source, &l.Kind, &l.Feed, &l.Summary, &l.AnalyzedAt, &l.CreatedAt, &l.UpdatedAt, &rank); err != nil {
			return nil, err
		}
```

- [ ] **Step 7: Build + tests**

```
go build ./...
go vet ./...
go test -tags=integration -count=1 ./links/... ./db/...
```

Expected: PASS. Existing tests don't reference summary, so they stay valid; new columns default to NULL/empty.

- [ ] **Step 8: Commit**

```
git add links/store.go
git commit -m "feat(links): Link/Save/Search/Similar/Get surface summary + analyzed_at"
```

---

## Task 3 — `links.StripHTML`

**Files:**
- Create: `links/striphtml.go`
- Create: `links/striphtml_test.go`

- [ ] **Step 1: Write failing tests**

Create `links/striphtml_test.go`:

```go
package links_test

import (
	"testing"

	"darek/links"
)

func TestStripHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text passes through", "hello world", "hello world"},
		{"single tag dropped", "<p>hello</p>", "hello"},
		{"nested tags dropped", "<div><p>hello <b>world</b></p></div>", "hello world"},
		{"entities decoded", "AT&amp;T &lt;3 &quot;tea&quot;", `AT&T <3 "tea"`},
		{"line breaks become spaces", "<p>one</p><p>two</p>", "one two"},
		{"collapses whitespace runs", "  hello   world\n\n\nfoo  ", "hello world foo"},
		{"empty input returns empty", "", ""},
		{"only tags returns empty", "<br><br/>", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := links.StripHTML(c.in)
			if got != c.want {
				t.Errorf("StripHTML(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Verify it fails**

```
go test ./links -run TestStripHTML
```

Expected: FAIL — `links.StripHTML` undefined.

- [ ] **Step 3: Implement `StripHTML`**

Create `links/striphtml.go`:

```go
package links

import (
	"html"
	"regexp"
	"strings"
)

var (
	tagRE   = regexp.MustCompile(`<[^>]*>`)
	wsRE    = regexp.MustCompile(`\s+`)
)

// StripHTML returns the visible text content of an HTML fragment with
// whitespace collapsed. Entities are decoded. Suitable for short bodies like
// RSS summaries; not a full parser.
func StripHTML(s string) string {
	if s == "" {
		return ""
	}
	// Replace tags with a single space so adjacent words don't run together.
	out := tagRE.ReplaceAllString(s, " ")
	out = html.UnescapeString(out)
	out = wsRE.ReplaceAllString(out, " ")
	return strings.TrimSpace(out)
}
```

- [ ] **Step 4: Run tests**

```
go test ./links -run TestStripHTML -v
```

Expected: all 8 cases PASS.

- [ ] **Step 5: Commit**

```
git add links/striphtml.go links/striphtml_test.go
git commit -m "feat(links): StripHTML — plain text from RSS HTML bodies"
```

---

## Task 4 — `Candidate.Summary` + `IngestOne` strips & stores

**Files:**
- Modify: `links/ingest.go`
- Modify: `links/ingest_test.go`

- [ ] **Step 1: Extend `Candidate`**

In `links/ingest.go`, replace the `Candidate` struct:

```go
type Candidate struct {
	URL     string
	Title   string
	Source  string
	Feed    string
	Kind    string
	Summary string // optional source-provided summary; HTML stripped before storage
}
```

- [ ] **Step 2: Pass `Summary` through `Save` in `IngestOne`**

Find the `store.Save(ctx, SaveInput{...})` call in `IngestOne`. Replace with:

```go
	id, err := store.Save(ctx, SaveInput{
		URL:     canon,
		Title:   c.Title,
		Source:  c.Source,
		Kind:    kind,
		Feed:    c.Feed,
		Summary: StripHTML(c.Summary),
	})
```

- [ ] **Step 3: Add a regression test**

Append to `links/ingest_test.go` (the existing file has build tag `//go:build integration`):

```go
func TestIngestOne_StoresStrippedSummary(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))
	ctx := context.Background()

	_, _, err := links.IngestOne(ctx, store, links.Candidate{
		URL:     "https://example.com/sum",
		Title:   "Hello",
		Source:  "freshrss",
		Summary: `<p>Hello <b>world</b>.</p><p>Second paragraph.</p>`,
	})
	require.NoError(t, err)

	got, err := store.Search(ctx, links.SearchOpts{Query: "Hello"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "Hello world. Second paragraph.", got[0].Summary)
	require.Nil(t, got[0].AnalyzedAt, "AnalyzedAt should still be nil — only AI sets it")
}
```

- [ ] **Step 4: Run tests**

```
go test -tags=integration -count=1 ./links/...
```

Expected: PASS — including the existing IngestOne tests (Candidate field is additive).

- [ ] **Step 5: Commit**

```
git add links/ingest.go links/ingest_test.go
git commit -m "feat(links): Candidate.Summary — IngestOne strips HTML and persists"
```

---

## Task 5 — FreshRSS: pass article summary into `Candidate`

**Files:**
- Modify: `freshrssimport/sync.go`

- [ ] **Step 1: Find the `Candidate` construction**

Inside `Sync`, locate the block:

```go
		_, _, err := links.IngestOne(ctx, store, links.Candidate{
			URL:    a.URL,
			Title:  a.Title,
			Source: "freshrss",
			Feed:   a.Feed,
		})
```

Replace with:

```go
		_, _, err := links.IngestOne(ctx, store, links.Candidate{
			URL:     a.URL,
			Title:   a.Title,
			Source:  "freshrss",
			Feed:    a.Feed,
			Summary: a.Summary,
		})
```

- [ ] **Step 2: Run tests**

```
go test -tags=integration -count=1 ./freshrssimport/... ./links/...
```

Expected: PASS — the existing fake-FreshRSS test still passes; the new field flows through.

- [ ] **Step 3: Commit**

```
git add freshrssimport/sync.go
git commit -m "feat(freshrssimport): pass article Summary into Candidate"
```

---

## Task 6 — `analyze` package

**Files:**
- Create: `analyze/analyze.go`
- Create: `analyze/analyze_test.go`

- [ ] **Step 1: Write failing tests**

Create `analyze/analyze_test.go`:

```go
package analyze_test

import (
	"context"
	"errors"
	"testing"

	"darek/analyze"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
)

// fakeChat returns a canned ChatCompletion content string.
type fakeChat struct {
	content string
	err     error
	gotMsgs []openai.ChatCompletionMessageParamUnion
}

func (f *fakeChat) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	f.gotMsgs = p.Messages
	if f.err != nil {
		return nil, f.err
	}
	return &openai.ChatCompletion{
		Choices: []openai.ChatCompletionChoice{
			{Message: openai.ChatCompletionMessage{Content: f.content}},
		},
	}, nil
}

func TestAnalyze_HappyPath(t *testing.T) {
	fc := &fakeChat{content: `{"summary":"It's a thing.","tags":["go","concurrency","performance"]}`}
	a := analyze.New(fc)

	out, err := a.Analyze(context.Background(), analyze.Input{
		Title: "A Thing",
		URL:   "https://example.com/a",
		Body:  "some body",
	})
	require.NoError(t, err)
	require.Equal(t, "It's a thing.", out.Summary)
	require.Equal(t, []string{"go", "concurrency", "performance"}, out.Tags)

	// Verify the prompt was constructed with title/url/body.
	require.GreaterOrEqual(t, len(fc.gotMsgs), 2, "system + user message expected")
}

func TestAnalyze_TagNormalization(t *testing.T) {
	fc := &fakeChat{content: `{"summary":"x","tags":["Go","go","  CONCURRENCY  ","",""]}`}
	a := analyze.New(fc)
	out, err := a.Analyze(context.Background(), analyze.Input{Title: "t", URL: "u"})
	require.NoError(t, err)
	require.Equal(t, []string{"go", "concurrency"}, out.Tags)
}

func TestAnalyze_TagCapAt7(t *testing.T) {
	fc := &fakeChat{content: `{"summary":"x","tags":["a","b","c","d","e","f","g","h","i"]}`}
	a := analyze.New(fc)
	out, err := a.Analyze(context.Background(), analyze.Input{Title: "t", URL: "u"})
	require.NoError(t, err)
	require.Len(t, out.Tags, 7)
}

func TestAnalyze_MalformedJSONError(t *testing.T) {
	fc := &fakeChat{content: `not json at all`}
	a := analyze.New(fc)
	_, err := a.Analyze(context.Background(), analyze.Input{Title: "t", URL: "u"})
	require.Error(t, err)
}

func TestAnalyze_EmptyBodyStillCallsModel(t *testing.T) {
	fc := &fakeChat{content: `{"summary":"based on title","tags":["x"]}`}
	a := analyze.New(fc)
	out, err := a.Analyze(context.Background(), analyze.Input{Title: "Just a title", URL: "u"})
	require.NoError(t, err)
	require.Equal(t, "based on title", out.Summary)
}

func TestAnalyze_PropagatesChatError(t *testing.T) {
	fc := &fakeChat{err: errors.New("boom")}
	a := analyze.New(fc)
	_, err := a.Analyze(context.Background(), analyze.Input{Title: "t", URL: "u"})
	require.Error(t, err)
}
```

- [ ] **Step 2: Verify it fails**

```
go test ./analyze/...
```

Expected: FAIL — package doesn't exist.

- [ ] **Step 3: Implement the analyzer**

Create `analyze/analyze.go`:

```go
package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
)

// Input is the payload an analyzer receives. Body may be empty (e.g. videos,
// tweets, links with no source-provided text); the prompt instructs the model
// to fall back to the title in that case.
type Input struct {
	Title string
	URL   string
	Body  string
}

// Output is the parsed model result. Summary is plain text, Tags are
// lowercase, deduped, and capped at 7.
type Output struct {
	Summary string
	Tags    []string
}

// Chat is the subset of *llm.Client used by Analyzer. Defined here so tests
// can supply a fake; *llm.Client satisfies it without changes.
type Chat interface {
	Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

// Analyzer wraps a Chat with a fixed prompt for link summarization + tagging.
type Analyzer struct {
	llm Chat
}

// New constructs an Analyzer.
func New(c Chat) *Analyzer { return &Analyzer{llm: c} }

const systemPrompt = `You are summarizing links the user is considering reading. Reply with strict JSON only:
{"summary": "...", "tags": ["...", "..."]}
The summary is 1-3 plain-text sentences, factual, no marketing language. Tags are 3-7 lowercase short topical labels (single word or hyphenated bigram). Do not invent facts. If the body is empty or unrelated to the title, summarize from the title alone.`

const maxBodyChars = 6000

// Analyze sends the input to the LLM and returns the parsed Output. The body
// is truncated to maxBodyChars before sending.
func (a *Analyzer) Analyze(ctx context.Context, in Input) (Output, error) {
	body := in.Body
	if len(body) > maxBodyChars {
		body = body[:maxBodyChars]
	}
	user := fmt.Sprintf("Title: %s\nURL: %s\nBody:\n%s", in.Title, in.URL, body)

	resp, err := a.llm.Chat(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(user),
		},
	})
	if err != nil {
		return Output{}, fmt.Errorf("chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return Output{}, fmt.Errorf("analyze: empty choices")
	}

	content := resp.Choices[0].Message.Content
	var raw struct {
		Summary string   `json:"summary"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return Output{}, fmt.Errorf("analyze: bad json from model: %w", err)
	}
	return Output{
		Summary: strings.TrimSpace(raw.Summary),
		Tags:    normalizeTags(raw.Tags),
	}, nil
}

// normalizeTags lowercases, trims, drops blanks, dedupes, and caps at 7.
func normalizeTags(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
		if len(out) >= 7 {
			break
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests**

```
go test ./analyze/... -v
```

Expected: all 6 cases PASS.

- [ ] **Step 5: Commit**

```
go vet ./...
git add analyze/analyze.go analyze/analyze_test.go
git commit -m "feat(analyze): LLM-backed link summarize + tag-propose"
```

---

## Task 7 — `LinksAnalyze` metric instrument

**Files:**
- Modify: `obs/metrics.go`
- Modify: `obs/metrics_test.go`

- [ ] **Step 1: Add the field + initializer**

In `obs/metrics.go`, find the "RSS ingest pipeline" block and add a third instrument:

```go
	// RSS ingest pipeline
	LinksIngest          metric.Int64Counter
	FreshRSSSyncDuration metric.Float64Histogram

	// AI analyze
	LinksAnalyze metric.Int64Counter
```

In `MetricsInstance`, add the initializer right after `FreshRSSSyncDuration`:

```go
			LinksAnalyze:         i64(m.Int64Counter("darek.links.analyze")),
```

- [ ] **Step 2: Extend the test**

In `obs/metrics_test.go`'s `TestMetricsInstance_HasNewInstruments`, append:

```go
	require.NotNil(t, m.LinksAnalyze, "LinksAnalyze not initialized")
```

- [ ] **Step 3: Run tests**

```
go test ./obs/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add obs/metrics.go obs/metrics_test.go
git commit -m "feat(obs): darek.links.analyze counter"
```

---

## Task 8 — `Server.analyze` field + `New` signature

**Files:**
- Modify: `cmd/darek/serve/server.go`
- Modify: `cmd/darek/serve/server_test.go`

- [ ] **Step 1: Extend the struct + constructor**

In `cmd/darek/serve/server.go`, replace the `Server` struct + `New` function with:

```go
type SyncFn func(ctx context.Context) (string, error)

type Server struct {
	store   *links.Store
	tmpl    *template.Template
	mux     *http.ServeMux
	sync    SyncFn
	analyze Analyzer
}

// Analyzer is the subset of *analyze.Analyzer used by the HTTP server.
// Defined as an interface so tests can supply a fake.
type Analyzer interface {
	Analyze(ctx context.Context, in analyze.Input) (analyze.Output, error)
}

// New constructs a Server. If sync is nil, /sync returns 501.
// If analyzer is nil, /links/{id}/analyze returns 501 and the UI hides the button.
func New(store *links.Store, sync SyncFn, analyzer Analyzer) (*Server, error) {
	t, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{store: store, tmpl: t, mux: http.NewServeMux(), sync: sync, analyze: analyzer}
	s.routes()
	return s, nil
}
```

Add `"darek/analyze"` to the imports.

- [ ] **Step 2: Update existing test**

In `cmd/darek/serve/server_test.go`, change every `serve.New(nil, nil)` to `serve.New(nil, nil, nil)`.

- [ ] **Step 3: Verify**

```
go build ./...
go vet ./...
go test ./cmd/darek/serve/...
```

Expected: clean. The `routes()` method doesn't yet register the analyze route — that's Task 9.

- [ ] **Step 4: Commit**

```
git add cmd/darek/serve/server.go cmd/darek/serve/server_test.go
git commit -m "feat(serve): Server.analyze field + Analyzer interface"
```

---

## Task 9 — `handleAnalyze` + view-model + route

**Files:**
- Modify: `cmd/darek/serve/handlers.go`
- Modify: `cmd/darek/serve/server.go`

- [ ] **Step 1: Extend `linkVM`**

In `cmd/darek/serve/handlers.go`, replace the `linkVM` struct with:

```go
type linkVM struct {
	ID             string
	URL            string
	Title          string
	Kind           string
	Feed           string
	Notes          string
	Tags           []string
	Rating         *int
	Summary        string
	AnalyzedAt     *time.Time
	AnalyzeEnabled bool
	RelTime        string
	RatingButtons  []ratingBtn
	AllKinds       []string
}
```

- [ ] **Step 2: Update `toLinkVM` to take an `analyzeEnabled` flag**

Replace the existing `toLinkVM` with:

```go
func toLinkVM(l links.Link, analyzeEnabled bool) linkVM {
	rb := make([]ratingBtn, 5)
	cur := 0
	if l.Rating != nil {
		cur = *l.Rating
	}
	for i := 0; i < 5; i++ {
		rb[i] = ratingBtn{Value: i + 1, Filled: i < cur}
	}
	return linkVM{
		ID:             l.ID.String(),
		URL:            l.URL,
		Title:          l.Title,
		Kind:           l.Kind,
		Feed:           l.Feed,
		Notes:          l.Notes,
		Tags:           l.Tags,
		Rating:         l.Rating,
		Summary:        l.Summary,
		AnalyzedAt:     l.AnalyzedAt,
		AnalyzeEnabled: analyzeEnabled,
		RelTime:        relTime(l.UpdatedAt),
		RatingButtons:  rb,
		AllKinds:       []string{"article", "video", "tweet", "podcast", "other"},
	}
}
```

- [ ] **Step 3: Update every `toLinkVM(l)` callsite**

Search/replace `toLinkVM(l)` and `toLinkVM(cur)` → `toLinkVM(l, s.analyze != nil)` / `toLinkVM(cur, s.analyze != nil)` across `handlers.go`. There are 5 callsites: `handleList`, `handleRating`, `handleTags`, `handleNotes`, `handleKind`. The `s` receiver must be in scope (it is for all of them).

- [ ] **Step 4: Add `handleAnalyze`**

Append to `handlers.go`. Add `"darek/analyze"`, `"darek/obs"`, `"go.opentelemetry.io/otel/attribute"`, and `"go.opentelemetry.io/otel/metric"` to the imports if not already present (some are; check first).

```go
func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if s.analyze == nil {
		http.Error(w, "analyze not configured", http.StatusNotImplemented)
		return
	}
	cur, err := s.fetchOne(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out, err := s.analyze.Analyze(r.Context(), analyze.Input{
		Title: cur.Title, URL: cur.URL, Body: cur.Summary,
	})
	if err != nil {
		if m, _ := obs.MetricsInstance(); m != nil {
			m.LinksAnalyze.Add(r.Context(), 1, metric.WithAttributes(attribute.String("outcome", "error")))
		}
		// Render the row with an inline error in the summary slot.
		cur.Summary = fmt.Sprintf("analysis failed: %v", err)
		_ = s.tmpl.ExecuteTemplate(w, "_row.html", toLinkVM(cur, true))
		return
	}

	if _, err := s.store.Pool().Exec(r.Context(), `
		UPDATE links
		   SET summary     = $2,
		       tags        = ARRAY(SELECT DISTINCT unnest(tags || $3::text[])),
		       analyzed_at = now(),
		       updated_at  = now()
		 WHERE id = $1`, id, out.Summary, out.Tags); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if m, _ := obs.MetricsInstance(); m != nil {
		m.LinksAnalyze.Add(r.Context(), 1, metric.WithAttributes(attribute.String("outcome", "ok")))
	}

	cur, err = s.fetchOne(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.tmpl.ExecuteTemplate(w, "_row.html", toLinkVM(cur, true)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
```

- [ ] **Step 5: Wire the route**

In `cmd/darek/serve/server.go` `routes()`, add:

```go
	s.mux.HandleFunc("POST /links/{id}/analyze", s.handleAnalyze)
```

- [ ] **Step 6: Verify**

```
go build ./...
go vet ./...
go test ./...
```

Expected: clean. Existing serve smoke tests still pass.

- [ ] **Step 7: Commit**

```
git add cmd/darek/serve
git commit -m "feat(serve): /links/{id}/analyze handler + view-model wiring"
```

---

## Task 10 — `_row.html` summary block + Analyze button + CSS

**Files:**
- Modify: `cmd/darek/serve/templates/_row.html`
- Modify: `cmd/darek/serve/static/style.css`

- [ ] **Step 1: Add summary block + analyze button**

Replace the contents of `cmd/darek/serve/templates/_row.html` with:

```html
{{define "_row.html"}}
<div class="row" id="row-{{.ID}}">
  {{template "_kind.html" .}}
  <a class="title" href="{{.URL}}" target="_blank" rel="noopener">{{if .Title}}{{.Title}}{{else}}{{.URL}}{{end}}</a>
  <div class="meta">
    {{if .Feed}}<span class="feed">{{.Feed}}</span><span class="dot">·</span>{{end}}
    <span>{{.RelTime}}</span>
    <span class="dot">·</span>
    <div class="stars" id="rating-{{.ID}}">
      {{range $n := .RatingButtons}}
        <button class="{{if $n.Filled}}filled{{end}}" hx-post="/links/{{$.ID}}/rating?value={{$n.Value}}" hx-target="#rating-{{$.ID}}" hx-swap="outerHTML" title="rate {{$n.Value}}/5">
          {{if $n.Filled}}★{{else}}☆{{end}}
        </button>
      {{end}}
    </div>
    {{if .AnalyzeEnabled}}
      <span class="dot">·</span>
      <button class="analyze" hx-post="/links/{{.ID}}/analyze" hx-target="#row-{{.ID}}" hx-swap="outerHTML" hx-indicator="#row-{{.ID}} .analyze">
        {{if .AnalyzedAt}}re-analyze{{else}}analyze{{end}}
      </button>
    {{end}}
  </div>
  {{if .Summary}}
    <div class="summary">{{.Summary}}</div>
  {{end}}
  <div class="tags" id="tags-{{.ID}}">
    {{range .Tags}}
      <span class="tag">{{.}}
        <form hx-post="/links/{{$.ID}}/tags" hx-target="#tags-{{$.ID}}" hx-swap="outerHTML" hx-vals='{"op":"remove"}'>
          <input type="hidden" name="tag" value="{{.}}">
          <button type="submit" title="remove">×</button>
        </form>
      </span>
    {{end}}
    <form hx-post="/links/{{.ID}}/tags" hx-target="#tags-{{.ID}}" hx-swap="outerHTML" hx-vals='{"op":"add"}'>
      <input type="text" name="tag" placeholder="+ tag">
    </form>
  </div>
  <div class="notes" id="notes-{{.ID}}">
    <details>
      <summary>{{if .Notes}}{{.Notes}}{{else}}+ add notes{{end}}</summary>
      <form hx-post="/links/{{.ID}}/notes" hx-target="#notes-{{.ID}}" hx-swap="outerHTML">
        <textarea name="notes" placeholder="why does this matter? what did you take from it?">{{.Notes}}</textarea>
        <button type="submit">save</button>
      </form>
    </details>
  </div>
</div>
{{end}}
```

The grid in `style.css` already has `grid-template-areas` listing `tags` and `notes` after `meta`. We need to add a `summary` row to that grid. Update CSS accordingly:

- [ ] **Step 2: Update grid + add new styles**

In `cmd/darek/serve/static/style.css`, find the `.row` selector (defines `grid-template-areas`). Replace it with:

```css
.row {
  padding: .9rem 1rem;
  display: grid;
  grid-template-columns: auto 1fr;
  grid-template-areas:
    "kind  title"
    ".     meta"
    ".     summary"
    ".     tags"
    ".     notes";
  column-gap: .75rem;
  row-gap: .35rem;
  border-bottom: 1px solid var(--border);
  transition: background .1s;
}
```

Append at the end of the file:

```css
/* analyze button + summary block */
.row .summary {
  grid-area: summary;
  font-size: .9rem;
  line-height: 1.5;
  color: var(--text-dim);
  font-style: italic;
  border-left: 2px solid var(--border);
  padding: .15rem 0 .15rem .65rem;
}

.meta button.analyze {
  font-size: .78rem;
  padding: .15rem .55rem;
  border-radius: 999px;
  background: transparent;
  border: 1px solid var(--border);
  color: var(--text-dim);
  cursor: pointer;
  transition: all .12s;
}
.meta button.analyze:hover {
  border-color: var(--accent);
  color: var(--accent);
}
.meta button.analyze.htmx-request {
  pointer-events: none;
  opacity: .6;
}
.meta button.analyze.htmx-request::after {
  content: " …";
}
```

- [ ] **Step 3: Verify**

```
go build ./...
go test ./cmd/darek/serve/...
```

Expected: PASS — templates parse at startup, so an unrelated typo would break `parseTemplates`.

- [ ] **Step 4: Commit**

```
git add cmd/darek/serve/templates/_row.html cmd/darek/serve/static/style.css
git commit -m "style(serve): summary block + analyze button (with htmx-request loading)"
```

---

## Task 11 — Wire LLM client + analyzer into `darek serve`

**Files:**
- Modify: `cmd/darek/serve.go`

- [ ] **Step 1: Build the LLM client unconditionally**

The current `runServe` only constructs a `*llm.Client` inside the FreshRSS branch. Lift it up so `darek serve` always has one when an OpenAI key is configured.

Replace the `runServe` body. The full new function:

```go
func runServe(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if cfg.Server.Bind == "" {
		cfg.Server.Bind = "127.0.0.1:7777"
	}
	if cfg.FreshRSS.SyncInterval == 0 {
		cfg.FreshRSS.SyncInterval = 15 * time.Minute
	}

	dsn, err := config.ResolveSecret("env:" + cfg.Postgres.URLEnv)
	if err != nil {
		return err
	}

	_, otelShutdown, err := obs.Init(ctx, obs.Options{
		ServiceName: cfg.OTEL.ServiceName,
		Endpoint:    cfg.OTEL.ExporterEndpoint,
		Insecure:    cfg.OTEL.Insecure,
	})
	if err != nil {
		return fmt.Errorf("otel init: %w", err)
	}
	defer func() { _ = otelShutdown(context.Background()) }()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer pool.Close()

	if err := obs.RegisterPoolGauges(pool); err != nil {
		fmt.Fprintf(os.Stderr, "warn: register pool gauges: %v\n", err)
	}

	store := links.NewStore(pool)

	// Build the LLM client + analyzer if OpenAI is configured.
	var analyzer serve.Analyzer
	if apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv); err == nil && apiKey != "" {
		llmClient, err := llm.New(llm.Options{
			APIKey:  apiKey,
			BaseURL: cfg.OpenAI.BaseURL,
			Model:   cfg.OpenAI.Model,
			Timeout: cfg.Agent.LLMTimeout,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: llm client: %v (analyze button disabled)\n", err)
		} else {
			analyzer = analyze.New(llmClient)
		}
	} else {
		fmt.Fprintf(os.Stderr, "info: openai not configured, analyze button disabled\n")
	}

	// Build the optional sync function — only if FreshRSS is configured.
	var sync serve.SyncFn
	if cfg.FreshRSS.BaseURL != "" {
		password, err := config.ResolveSecret("env:" + cfg.FreshRSS.PasswordEnv)
		if err != nil {
			return fmt.Errorf("freshrss password: %w", err)
		}
		fr, err := freshrss.New(freshrss.Options{
			BaseURL:  cfg.FreshRSS.BaseURL,
			Username: cfg.FreshRSS.Username,
			Password: password,
		})
		if err != nil {
			return fmt.Errorf("freshrss client: %w", err)
		}
		sync = func(ctx context.Context) (string, error) {
			res, err := freshrssimport.Sync(ctx, fr, store)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("imported=%d marked_read=%d errors=%d",
				res.Imported, res.MarkedRead, len(res.Errors)), nil
		}
	}

	srv, err := serve.New(store, sync, analyzer)
	if err != nil {
		return err
	}

	if sync != nil && cfg.FreshRSS.SyncInterval > 0 {
		go runSyncLoop(ctx, sync, cfg.FreshRSS.SyncInterval)
	}

	fmt.Fprintf(os.Stderr, "darek serve listening on %s\n", cfg.Server.Bind)
	return srv.Run(ctx, cfg.Server.Bind)
}
```

Add `"darek/analyze"` and `"darek/llm"` to the imports.

- [ ] **Step 2: Verify**

```
go build ./...
go vet ./...
go test ./...
```

Expected: clean.

- [ ] **Step 3: Commit**

```
git add cmd/darek/serve.go
git commit -m "feat(cmd): wire analyze.Analyzer into darek serve"
```

---

## Task 12 — Manual verification

This is the smoke test. Document the result.

- [ ] **Step 1: Bring up the stack and apply the new migration**

```
make up
make obs-up
make build
./darek migrate
```

Expected: migration applies the `summary` and `analyzed_at` columns without error.

- [ ] **Step 2: Generate content and exercise the UI**

```
./darek serve &
open http://127.0.0.1:7777/
```

Then in the browser:

1. Click "sync now" — articles import. Each row's source-provided summary (HTML-stripped) should already be visible under the meta line.
2. Click "analyze" on a row.
3. Confirm: a small loader appears next to the button while the call runs (~2-5s); the row swaps in with a refreshed summary, the proposed tags appended to the existing tags chip list, and the button now reads "re-analyze".
4. Click "re-analyze" — confirm summary updates; tags merge (no duplicates).
5. Add a YouTube link manually (no body); click "analyze" — summary should fall back to a title-based one-liner.

- [ ] **Step 3: Confirm metrics**

```
curl -s http://localhost:8889/metrics | grep darek_links_analyze
curl -s http://localhost:8889/metrics | grep darek_tokens_input
```

Expected:
- `darek_links_analyze_total{outcome="ok"} N` where N matches your click count.
- `darek_tokens_input_total{model="..."}` increases per click.

- [ ] **Step 4: README update**

Append to the "RSS inbox + web UI" section in `README.md`:

```markdown
Each row has an **analyze** button that asks OpenAI to summarize the link and propose tags. Click it; the row updates in place. Tags merge into existing tags; the proposed summary overwrites whatever the source provided.
```

```
git add README.md
git commit -m "docs: README — analyze button"
```

---

## Self-review notes

**Spec coverage:**
- §3 Schema → Task 1.
- §4 `Link`/`SaveInput`/`Save`/`Search`/`Similar`/`Get`/`IngestOne`/`StripHTML` → Tasks 2, 3, 4.
- §5 freshrssimport → Task 5.
- §6 analyze package → Task 6.
- §7 server (struct, handler, route, view-model, templates) → Tasks 8, 9, 10.
- §7 wiring in `cmd/darek/serve.go` → Task 11.
- §8 LinksAnalyze counter → Task 7.
- §9 testing — across the relevant tasks (analyze unit tests, ingest integration test, manual verification).
- §10 risks — informational; the additive migration and best-effort metric pattern handle them.

**Type consistency:**
- `links.Link.Summary` / `links.Link.AnalyzedAt` introduced in Task 2 and used through Tasks 4, 9.
- `links.Candidate.Summary` introduced in Task 4 and used in Task 5.
- `analyze.Input{Title, URL, Body}` and `analyze.Output{Summary, Tags}` defined in Task 6 and used in Task 9.
- `serve.Analyzer` interface defined in Task 8; satisfied by `*analyze.Analyzer` (returns `analyze.Output`); used in Task 9 handler and Task 11 wiring.
- `obs.Metrics.LinksAnalyze` defined in Task 7; used in Task 9.

**Open notes:**
- Task 9's `toLinkVM` signature change touches 5 existing handler callsites. The plan lists them inline; no surprises.
- The analyzer's prompt is in code (constant in `analyze/analyze.go`); changing it is a code-change-and-redeploy. This matches the spec ("no prompt customization in the UI").
- Task 6 deliberately doesn't force OpenAI's JSON mode (`response_format`). The prompt asks for JSON; modern models comply. If real-world malformed-JSON rates become a problem, switching to JSON mode is a one-field change in the params.
