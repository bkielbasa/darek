# Auto-analyze video links from RSS / Todoist Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When FreshRSS or Todoist sync ingests a new YouTube video link, automatically fetch the transcript via `tools/youtube`, run `analyze` against the transcript, and persist summary + tags. The manual "Analyze" button in the inbox UI also switches to using the transcript for video URLs.

**Architecture:** Two layers. (A) `analyze.VideoAwareAnalyzer` wraps `*analyze.Analyzer` and a `Transcriber` (satisfied by `*youtube.Client`) — for YouTube URLs it fetches the transcript and overwrites `Input.Body` before delegating. (B) `freshrssimport.Sync` / `todoistimport.Sync` accept an optional `OnVideoIngestedFunc` callback fired when `IngestOne` returns `isNew=true && kind=="video"`. The wiring code in `cmd/darek` constructs both and connects them; sync packages stay free of `analyze` / `youtube` imports.

**Tech Stack:** Go stdlib + existing project libraries (`pgx`, `openai-go`, `otel`). Tests use `github.com/stretchr/testify/require`.

**Design source:** [docs/specs/2026-05-04-video-link-auto-analyze-design.md](../specs/2026-05-04-video-link-auto-analyze-design.md), approved 2026-05-04.

**Dependency:** This plan depends on [docs/plans/2026-05-04-youtube-transcript-tool.md](2026-05-04-youtube-transcript-tool.md) being implemented first — it reuses `youtube.Client`, `youtube.Result`, and `youtube.ExtractVideoID`.

**Out of scope (deferred):** Backfilling existing un-analyzed videos, persisting raw transcripts, sync-side concurrency tuning, non-YouTube transcript sources, per-user `lang` preference.

---

## File Map

| Path | Responsibility |
|---|---|
| `links/ingest.go` | (modify) `IngestOne` returns `(uuid.UUID, bool, string, error)` — adds resolved kind. |
| `links/ingest_test.go` | (modify) update existing tests for the new return value. |
| `links/store.go` | (modify) add `*Store.ApplyAnalysis(ctx, id, summary, tags)` method. |
| `links/store_test.go` | (modify) test for `ApplyAnalysis`. |
| `cmd/darek/serve/handlers.go` | (modify) `handleAnalyze` calls `store.ApplyAnalysis`; metric calls add `trigger="manual"` label. |
| `analyze/video_aware.go` | (create) `Transcriber` interface, `VideoAwareAnalyzer`, `NewVideoAware`, `Analyze` method. |
| `analyze/video_aware_test.go` | (create) happy YouTube URL, non-YouTube URL, transcript fetch error. |
| `freshrssimport/sync.go` | (modify) add `OnVideoIngestedFunc` type + 4th positional param to `Sync`; `processArticle` fires the callback for new video rows. |
| `freshrssimport/sync_test.go` | (modify) extend with callback assertions. |
| `todoistimport/sync.go` | (modify) same shape as freshrssimport. |
| `todoistimport/sync_test.go` | (modify) extend with callback assertions. |
| `cmd/darek/serve.go` | (modify) build `youtube.Client` + `VideoAwareAnalyzer` + `onVideo` closure; pass into both periodic syncs. |
| `cmd/darek/freshrss.go` | (modify) same closure for the `darek freshrss sync` CLI run. |
| `cmd/darek/todoist.go` | (modify) same closure for the `darek todoist sync` CLI run. |

> **Note** — the spec said sync takes a `SyncOptions` struct, but the existing code uses positional args (`Sync(ctx, c, store)`). This plan adds a 4th positional param `onVideoIngested OnVideoIngestedFunc` instead of refactoring to a struct. Smaller diff, same behavior.

---

## Task 1 — `links.IngestOne` returns kind

**Files:**
- Modify: `links/ingest.go`
- Modify: `links/ingest_test.go`
- Modify: `freshrssimport/sync.go` (caller)
- Modify: `todoistimport/sync.go` (caller)

`IngestOne` already classifies the URL internally; we just need to return the resolved kind so callers can branch on `"video"` without re-classifying.

- [ ] **Step 1: Update existing tests for the new return signature**

Find and update every `_, _, err := links.IngestOne(...)` and `id, isNew, err := links.IngestOne(...)` call site in `links/ingest_test.go` to capture a 3rd `kind` return value. The simplest mechanical change is `id, isNew, kind, err := links.IngestOne(...)` and ignore `kind` with `_ = kind` if not asserted, OR add an assertion if the test cares.

Search-and-update:

```bash
cd /Users/bklimczak/Projects/darek && grep -n "links.IngestOne\|IngestOne(" links/ingest_test.go
```

For each match, replace the LHS pattern `id, isNew, err :=` with `id, isNew, kind, err :=` (or `_, _, _, err :=` etc.), and add at least one new assertion in a single test that the returned kind is `"video"` for a youtube URL and `"article"` for a generic URL:

```go
func TestIngestOne_ReturnsKind(t *testing.T) {
	pool, store, cleanup := newTestStore(t)
	defer cleanup()
	_ = pool

	tests := []struct {
		url      string
		wantKind string
	}{
		{"https://www.youtube.com/watch?v=abcDEF12345", "video"},
		{"https://example.com/some-article", "article"},
	}
	for _, tc := range tests {
		t.Run(tc.wantKind, func(t *testing.T) {
			_, _, kind, err := links.IngestOne(context.Background(), store, links.Candidate{
				URL:    tc.url,
				Title:  "t",
				Source: "user",
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantKind, kind)
		})
	}
}
```

(Use the existing test setup helper if `newTestStore` differs from this; the imports needed are `"context"`, `"testing"`, `"darek/links"`, `"github.com/stretchr/testify/require"`.)

- [ ] **Step 2: Run the test, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test ./links/ -run TestIngestOne_ReturnsKind -v`
Expected: FAIL — compile error: too many return values.

- [ ] **Step 3: Update `IngestOne` signature and body**

In `links/ingest.go`, change the function to return kind. Replace the existing `IngestOne` with:

```go
// IngestOne canonicalizes the URL, infers kind if unset, and upserts via the
// store. Returns the resulting link id, whether it was a brand-new row, and
// the resolved kind.
//
// On metrics failure, ingestion proceeds without recording (matches the
// "instrumentation never blocks real work" contract).
func IngestOne(ctx context.Context, store *Store, c Candidate) (uuid.UUID, bool, string, error) {
	if store == nil {
		return uuid.Nil, false, "", errors.New("links.IngestOne: store is required")
	}
	canon := Canonicalize(c.URL)
	if canon == "" {
		return uuid.Nil, false, "", fmt.Errorf("links.IngestOne: unparseable url %q", c.URL)
	}

	kind := c.Kind
	if kind == "" {
		kind = Classify(canon)
	}

	// Detect new vs upsert by checking pre-existence (cheap; the existing Save
	// already does this internally but doesn't surface the answer).
	isNew := false
	{
		var existingID uuid.UUID
		err := store.pool.QueryRow(ctx, `SELECT id FROM links WHERE url = $1`, canon).Scan(&existingID)
		if errors.Is(err, ErrNoRows()) {
			isNew = true
		} else if err != nil {
			return uuid.Nil, false, "", fmt.Errorf("links.IngestOne lookup: %w", err)
		}
	}

	id, err := store.Save(ctx, SaveInput{
		URL:     canon,
		Title:   c.Title,
		Source:  c.Source,
		Kind:    kind,
		Feed:    c.Feed,
		Summary: StripHTML(c.Summary),
	})
	if err != nil {
		if store.m != nil {
			store.m.LinksIngest.Add(ctx, 1, metric.WithAttributes(
				attribute.String("source", normalizeSource(c.Source)),
				attribute.String("kind", kind),
				attribute.String("outcome", "error"),
			))
		}
		return uuid.Nil, false, kind, err
	}
	if store.m != nil {
		store.m.LinksIngest.Add(ctx, 1, metric.WithAttributes(
			attribute.String("source", normalizeSource(c.Source)),
			attribute.String("kind", kind),
			attribute.String("outcome", "ok"),
		))
	}
	return id, isNew, kind, nil
}
```

- [ ] **Step 4: Update freshrssimport caller**

In `freshrssimport/sync.go`, find the line:

```go
_, _, err := links.IngestOne(ctx, store, links.Candidate{
```

Change to:

```go
_, _, _, err := links.IngestOne(ctx, store, links.Candidate{
```

- [ ] **Step 5: Update todoistimport caller**

In `todoistimport/sync.go`, find:

```go
id, _, err := links.IngestOne(ctx, store, links.Candidate{
```

Change to:

```go
id, _, _, err := links.IngestOne(ctx, store, links.Candidate{
```

- [ ] **Step 6: Run all tests in affected packages**

Run: `cd /Users/bklimczak/Projects/darek && go test ./links/ ./freshrssimport/ ./todoistimport/ -v`
Expected: PASS.

- [ ] **Step 7: Build to confirm no other callers**

Run: `cd /Users/bklimczak/Projects/darek && go build ./...`
Expected: clean build.

- [ ] **Step 8: Commit**

```bash
git add links/ingest.go links/ingest_test.go freshrssimport/sync.go todoistimport/sync.go
git commit -m "refactor(links): IngestOne returns resolved kind"
```

---

## Task 2 — `links.Store.ApplyAnalysis` method (TDD)

**Files:**
- Modify: `links/store.go`
- Modify: `links/store_test.go`

Extracts the analyze-write SQL into a reusable method. Same SQL as the existing manual-analyze handler.

- [ ] **Step 1: Write the failing test**

Append to `links/store_test.go`:

```go
func TestStore_ApplyAnalysis(t *testing.T) {
	pool, store, cleanup := newTestStore(t)
	defer cleanup()
	_ = pool

	// Seed a row with one existing tag.
	id, err := store.Save(context.Background(), links.SaveInput{
		URL:    "https://example.com/x",
		Title:  "X",
		Tags:   []string{"existing"},
		Source: "user",
	})
	require.NoError(t, err)

	err = store.ApplyAnalysis(context.Background(), id, "ai summary", []string{"new", "existing"})
	require.NoError(t, err)

	got, err := store.Get(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "ai summary", got.Summary)
	require.NotNil(t, got.AnalyzedAt)
	require.ElementsMatch(t, []string{"existing", "new"}, got.Tags)
}
```

(If `newTestStore` and `store.Get` aren't the exact existing helper names in `store_test.go`, adapt to whatever the file already uses — search with `grep -n "func newTest\|func.*Store.*test" links/store_test.go`.)

- [ ] **Step 2: Run the test, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test ./links/ -run TestStore_ApplyAnalysis -v`
Expected: FAIL — `store.ApplyAnalysis undefined`.

- [ ] **Step 3: Implement `ApplyAnalysis`**

Add to `links/store.go` (near other `*Store` methods):

```go
// ApplyAnalysis writes the AI analyze result for a link: overwrites summary,
// merges tags into existing tags (deduped), and bumps analyzed_at.
//
// Takes primitive types (not analyze.Output) to avoid a links → analyze import.
// Callers unpack at the call site.
func (s *Store) ApplyAnalysis(ctx context.Context, id uuid.UUID, summary string, tags []string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE links
		   SET summary     = $2,
		       tags        = ARRAY(SELECT DISTINCT unnest(tags || $3::text[])),
		       analyzed_at = now(),
		       updated_at  = now()
		 WHERE id = $1`, id, summary, tags)
	return err
}
```

- [ ] **Step 4: Run the test, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./links/ -run TestStore_ApplyAnalysis -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add links/store.go links/store_test.go
git commit -m "feat(links): Store.ApplyAnalysis method"
```

---

## Task 3 — `serve.handleAnalyze` uses `ApplyAnalysis` + `trigger` label

**Files:**
- Modify: `cmd/darek/serve/handlers.go`

Refactor only — pulls the inline SQL into the new `Store.ApplyAnalysis` and adds the `trigger="manual"` label to both metric calls so the `darek.links.analyze` counter can split manual clicks from sync auto-analyze.

- [ ] **Step 1: Replace the inline SQL and metric calls**

Open `cmd/darek/serve/handlers.go`. Locate `handleAnalyze` (around line 381). Replace the body from `out, err := s.analyze.Analyze(...)` through the closing curly so it reads:

```go
	out, err := s.analyze.Analyze(r.Context(), analyze.Input{
		Title: cur.Title, URL: cur.URL, Body: cur.Summary,
	})
	if err != nil {
		if m, _ := obs.MetricsInstance(); m != nil {
			m.LinksAnalyze.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("outcome", "error"),
				attribute.String("trigger", "manual"),
			))
		}
		// Render the row with an inline error in the summary slot.
		cur.Summary = fmt.Sprintf("analysis failed: %v", err)
		_ = s.tmpl.ExecuteTemplate(w, "_row.html", toLinkVM(cur, true))
		return
	}

	if err := s.store.ApplyAnalysis(r.Context(), id, out.Summary, out.Tags); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if m, _ := obs.MetricsInstance(); m != nil {
		m.LinksAnalyze.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("outcome", "ok"),
			attribute.String("trigger", "manual"),
		))
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

- [ ] **Step 2: Run the existing handler tests**

Run: `cd /Users/bklimczak/Projects/darek && go test ./cmd/darek/serve/... -v`
Expected: PASS — behavior is identical, just the SQL moved.

- [ ] **Step 3: Build**

Run: `cd /Users/bklimczak/Projects/darek && go build ./...`
Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add cmd/darek/serve/handlers.go
git commit -m "refactor(serve): handleAnalyze uses Store.ApplyAnalysis; add trigger=manual label"
```

---

## Task 4 — `analyze.VideoAwareAnalyzer` (TDD)

**Files:**
- Create: `analyze/video_aware.go`
- Create: `analyze/video_aware_test.go`

A wrapper that, for YouTube URLs, fetches the transcript via a `Transcriber` interface and overwrites `Input.Body` before delegating to the inner `*Analyzer`. For non-YouTube URLs it delegates unchanged.

- [ ] **Step 1: Create the file with stubs**

Create `analyze/video_aware.go`:

```go
package analyze

import (
	"context"
	"fmt"

	"darek/tools/youtube"
)

// Transcriber is the subset of *youtube.Client used by VideoAwareAnalyzer.
// Defined as an interface so tests can supply a fake; *youtube.Client
// satisfies it without changes.
type Transcriber interface {
	Fetch(ctx context.Context, rawURL, lang string) (youtube.Result, error)
}

// VideoAwareAnalyzer wraps *Analyzer. For YouTube URLs (anything
// youtube.ExtractVideoID accepts) it fetches the transcript and uses it as
// Input.Body, ignoring whatever Body the caller passed. For non-YouTube
// URLs it delegates straight to the inner Analyzer.
type VideoAwareAnalyzer struct {
	inner       *Analyzer
	transcriber Transcriber
}

// NewVideoAware constructs a VideoAwareAnalyzer.
func NewVideoAware(inner *Analyzer, t Transcriber) *VideoAwareAnalyzer {
	return &VideoAwareAnalyzer{inner: inner, transcriber: t}
}

// Analyze fetches the transcript for YouTube URLs (replacing Input.Body) and
// delegates to the inner Analyzer.
func (v *VideoAwareAnalyzer) Analyze(ctx context.Context, in Input) (Output, error) {
	if _, err := youtube.ExtractVideoID(in.URL); err == nil {
		res, terr := v.transcriber.Fetch(ctx, in.URL, "")
		if terr != nil {
			return Output{}, fmt.Errorf("youtube transcript: %w", terr)
		}
		in.Body = res.Text
	}
	return v.inner.Analyze(ctx, in)
}
```

- [ ] **Step 2: Write the tests**

Create `analyze/video_aware_test.go`:

```go
package analyze

import (
	"context"
	"errors"
	"strings"
	"testing"

	"darek/tools/youtube"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
)

// fakeTranscriber records the URL it was asked for and returns a fixed result.
type fakeTranscriber struct {
	res    youtube.Result
	err    error
	called int
	gotURL string
}

func (f *fakeTranscriber) Fetch(ctx context.Context, rawURL, lang string) (youtube.Result, error) {
	f.called++
	f.gotURL = rawURL
	return f.res, f.err
}

// fakeChat records the user message body it sees and returns a fixed JSON response.
type fakeChat struct {
	gotUserContent string
	resp           string
	err            error
}

func (f *fakeChat) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	for _, msg := range params.Messages {
		// Find the user message; tests assert what reached the model.
		if u, ok := msg.OfUser.GetContent().AsAny().(*string); ok {
			f.gotUserContent = *u
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return &openai.ChatCompletion{
		Choices: []openai.ChatCompletionChoice{
			{Message: openai.ChatCompletionMessage{Content: f.resp}},
		},
	}, nil
}

func TestVideoAware_YouTubeURL_UsesTranscript(t *testing.T) {
	tr := &fakeTranscriber{res: youtube.Result{Text: "TRANSCRIPT BODY"}}
	chat := &fakeChat{resp: `{"summary":"s","tags":["a","b"]}`}
	v := NewVideoAware(New(chat), tr)

	out, err := v.Analyze(context.Background(), Input{
		Title: "vid",
		URL:   "https://www.youtube.com/watch?v=abcDEF12345",
		Body:  "ignored YT description",
	})
	require.NoError(t, err)
	require.Equal(t, "s", out.Summary)
	require.Equal(t, 1, tr.called)
	require.Equal(t, "https://www.youtube.com/watch?v=abcDEF12345", tr.gotURL)
	require.Contains(t, chat.gotUserContent, "TRANSCRIPT BODY")
	require.NotContains(t, chat.gotUserContent, "ignored YT description")
}

func TestVideoAware_NonYouTubeURL_PassesThrough(t *testing.T) {
	tr := &fakeTranscriber{}
	chat := &fakeChat{resp: `{"summary":"s","tags":[]}`}
	v := NewVideoAware(New(chat), tr)

	_, err := v.Analyze(context.Background(), Input{
		Title: "art",
		URL:   "https://example.com/an-article",
		Body:  "article body",
	})
	require.NoError(t, err)
	require.Equal(t, 0, tr.called, "transcriber must not be called for non-YouTube URLs")
	require.Contains(t, chat.gotUserContent, "article body")
}

func TestVideoAware_TranscriptFetchError(t *testing.T) {
	tr := &fakeTranscriber{err: errors.New("no captions available")}
	chat := &fakeChat{resp: `{"summary":"s","tags":[]}`}
	v := NewVideoAware(New(chat), tr)

	_, err := v.Analyze(context.Background(), Input{
		Title: "vid",
		URL:   "https://www.youtube.com/watch?v=abcDEF12345",
	})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "youtube transcript"), err)
	require.True(t, strings.Contains(err.Error(), "no captions available"), err)
	require.Equal(t, "", chat.gotUserContent, "chat must not be called when transcript fetch fails")
}
```

> **If `fakeChat` extraction differs in this codebase** — the existing `analyze/analyze_test.go` already has a `fakeChat` (or similarly-named) helper that satisfies the `Chat` interface. Look there: `grep -n "type.*Chat\|fakeChat\|stubChat" analyze/analyze_test.go`. If a usable helper exists, import it (same package) instead of redefining. The test above falls back to redefining if the existing one isn't sufficient. The Step 2 code will compile only if both files share the same package (`analyze`), so duplicates will cause a build error — adapt the new test's fake names accordingly (`fakeChatVA`, etc.).

- [ ] **Step 3: Run the tests**

Run: `cd /Users/bklimczak/Projects/darek && go test ./analyze/ -run TestVideoAware -v`
Expected: PASS. If a name collision with the existing `fakeChat` blocks compile, rename the new one (e.g. `fakeVAChat`) and re-run.

- [ ] **Step 4: Commit**

```bash
git add analyze/video_aware.go analyze/video_aware_test.go
git commit -m "feat(analyze): VideoAwareAnalyzer wraps Analyzer with transcript fetch for YouTube URLs"
```

---

## Task 5 — `freshrssimport.Sync` accepts `OnVideoIngestedFunc`

**Files:**
- Modify: `freshrssimport/sync.go`
- Modify: `freshrssimport/sync_test.go`

Adds a 4th positional parameter to `Sync`. When non-nil and the ingested item is a brand-new video, the callback runs after `IngestOne`. Errors from the callback are appended to `res.Errors` (sync continues).

- [ ] **Step 1: Write the failing tests**

Find the existing test setup pattern in `freshrssimport/sync_test.go`:

```bash
cd /Users/bklimczak/Projects/darek && grep -n "func Test\|fakeFR\|FakeLister" freshrssimport/sync_test.go | head -20
```

Append three new tests, using whatever fake `Lister` and store helpers the file already provides. Pattern:

```go
func TestSync_OnVideoIngested_NewVideo(t *testing.T) {
	// Setup: fake lister returns one video URL + one article URL.
	fr := newFakeLister(t, []freshrss.Article{
		{ID: "1", URL: "https://www.youtube.com/watch?v=abcDEF12345", Title: "vid", Feed: "f"},
		{ID: "2", URL: "https://example.com/an-article", Title: "art", Feed: "f"},
	})
	store, cleanup := newTestStore(t)
	defer cleanup()

	type call struct {
		linkID uuid.UUID
		url    string
		title  string
	}
	var calls []call
	onVideo := func(ctx context.Context, id uuid.UUID, url, title string) error {
		calls = append(calls, call{id, url, title})
		return nil
	}

	res, err := freshrssimport.Sync(context.Background(), fr, store, onVideo)
	require.NoError(t, err)
	require.Equal(t, 2, res.Imported)
	require.Len(t, calls, 1, "callback fires exactly once for the video")
	require.Equal(t, "https://www.youtube.com/watch?v=abcDEF12345", calls[0].url)
	require.Equal(t, "vid", calls[0].title)
	require.NotEqual(t, uuid.Nil, calls[0].linkID)
}

func TestSync_OnVideoIngested_NotForExistingVideo(t *testing.T) {
	url := "https://www.youtube.com/watch?v=abcDEF12345"
	store, cleanup := newTestStore(t)
	defer cleanup()
	// Pre-ingest the same URL so the second sync sees it as not-new.
	_, _, _, err := links.IngestOne(context.Background(), store, links.Candidate{
		URL: url, Title: "vid", Source: "user",
	})
	require.NoError(t, err)

	fr := newFakeLister(t, []freshrss.Article{{ID: "1", URL: url, Title: "vid", Feed: "f"}})
	called := 0
	onVideo := func(ctx context.Context, id uuid.UUID, url, title string) error {
		called++
		return nil
	}
	_, err = freshrssimport.Sync(context.Background(), fr, store, onVideo)
	require.NoError(t, err)
	require.Equal(t, 0, called, "callback must not fire for existing rows")
}

func TestSync_OnVideoIngested_CallbackErrorContinues(t *testing.T) {
	fr := newFakeLister(t, []freshrss.Article{
		{ID: "1", URL: "https://www.youtube.com/watch?v=abcDEF12345", Title: "v1", Feed: "f"},
		{ID: "2", URL: "https://www.youtube.com/watch?v=zzzZZZ98765", Title: "v2", Feed: "f"},
	})
	store, cleanup := newTestStore(t)
	defer cleanup()

	onVideo := func(ctx context.Context, id uuid.UUID, url, title string) error {
		return errors.New("boom")
	}
	res, err := freshrssimport.Sync(context.Background(), fr, store, onVideo)
	require.NoError(t, err) // sync itself succeeds
	require.Equal(t, 2, res.Imported, "both videos still imported")
	require.GreaterOrEqual(t, len(res.Errors), 2, "both callback errors collected")
}

func TestSync_OnVideoIngested_NilCallbackOK(t *testing.T) {
	fr := newFakeLister(t, []freshrss.Article{
		{ID: "1", URL: "https://www.youtube.com/watch?v=abcDEF12345", Title: "v1", Feed: "f"},
	})
	store, cleanup := newTestStore(t)
	defer cleanup()

	res, err := freshrssimport.Sync(context.Background(), fr, store, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Imported)
}
```

(Adapt `newFakeLister` and `newTestStore` to the helper names actually present.)

Add the necessary imports if not already present: `"context"`, `"errors"`, `"github.com/google/uuid"`, `"darek/links"`.

- [ ] **Step 2: Run tests, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test ./freshrssimport/ -run TestSync_OnVideoIngested -v`
Expected: FAIL — compile error: too many arguments to `Sync`.

- [ ] **Step 3: Add the type and modify `Sync` + `processArticle`**

In `freshrssimport/sync.go`, add the type below the existing type declarations (e.g. after `Result`):

```go
// OnVideoIngestedFunc is called once per newly-ingested video link
// (kind=="video", isNew=true). Errors are appended to Result.Errors but do
// not abort sync. Implementations are typically a closure that wraps an
// analyze step and persists summary+tags via *links.Store.ApplyAnalysis.
type OnVideoIngestedFunc func(ctx context.Context, linkID uuid.UUID, url, title string) error
```

Add `"github.com/google/uuid"` to imports if not already present.

Change the `Sync` signature to:

```go
func Sync(ctx context.Context, fr Lister, store *links.Store, onVideoIngested OnVideoIngestedFunc) (*Result, error) {
```

Inside the goroutine loop, pass `onVideoIngested` to `processArticle`:

```go
for i, a := range arts {
	g.Go(func() error {
		outcomes[i] = processArticle(gctx, fr, store, a, onVideoIngested)
		return nil
	})
}
```

Modify `processArticle` to accept the callback and fire it for new videos:

```go
func processArticle(ctx context.Context, fr Lister, store *links.Store, a freshrss.Article, onVideoIngested OnVideoIngestedFunc) articleOutcome {
	if a.URL == "" {
		return articleOutcome{Skipped: true}
	}
	id, isNew, kind, err := links.IngestOne(ctx, store, links.Candidate{
		URL:     a.URL,
		Title:   a.Title,
		Source:  "freshrss",
		Feed:    a.Feed,
		Summary: a.Summary,
	})
	if err != nil {
		return articleOutcome{Err: fmt.Errorf("ingest %s: %w", a.ID, err)}
	}

	o := articleOutcome{Imported: true}
	if err := fr.Mark(ctx, a.ID, freshrss.ActionMarkRead); err != nil {
		o.Err = fmt.Errorf("mark %s read: %w", a.ID, err)
	} else {
		o.MarkedRead = true
	}

	if isNew && kind == "video" && onVideoIngested != nil {
		if cbErr := onVideoIngested(ctx, id, a.URL, a.Title); cbErr != nil {
			// Append to existing error if mark-read also failed; otherwise set.
			if o.Err == nil {
				o.Err = fmt.Errorf("auto-analyze %s: %w", a.ID, cbErr)
			} else {
				o.Err = fmt.Errorf("%v; auto-analyze: %w", o.Err, cbErr)
			}
		}
	}
	return o
}
```

- [ ] **Step 4: Run all freshrssimport tests**

Run: `cd /Users/bklimczak/Projects/darek && go test ./freshrssimport/ -v`
Expected: PASS for new tests AND existing tests (pre-existing tests pass `nil` for the new param — but they currently call `Sync(ctx, fr, store)` with only 3 args so they're broken until updated).

- [ ] **Step 5: Update existing freshrssimport tests for the new signature**

Search for all `freshrssimport.Sync(` calls in `freshrssimport/sync_test.go` that pass 3 args and add a 4th `nil`:

```bash
grep -n "freshrssimport.Sync\|Sync(ctx" freshrssimport/sync_test.go
```

For every match like `Sync(ctx, fr, store)`, change to `Sync(ctx, fr, store, nil)`.

- [ ] **Step 6: Run tests again**

Run: `cd /Users/bklimczak/Projects/darek && go test ./freshrssimport/ -v`
Expected: ALL PASS (new + existing).

- [ ] **Step 7: Commit**

```bash
git add freshrssimport/sync.go freshrssimport/sync_test.go
git commit -m "feat(freshrssimport): OnVideoIngested callback for new video rows"
```

---

## Task 6 — `todoistimport.Sync` accepts `OnVideoIngestedFunc`

**Files:**
- Modify: `todoistimport/sync.go`
- Modify: `todoistimport/sync_test.go`

Same shape as Task 5. Mirrors freshrss; small differences in field names (`taskOutcome` instead of `articleOutcome`, `Lister.CompleteTask` instead of `Mark`).

- [ ] **Step 1: Write the failing tests**

Append to `todoistimport/sync_test.go` (using whatever fake `Lister` / store helper already exists):

```go
func TestTodoistSync_OnVideoIngested_NewVideo(t *testing.T) {
	c := newFakeTodoistLister(t, []todoist.Task{
		{ID: "1", Content: "watch this https://www.youtube.com/watch?v=abcDEF12345"},
		{ID: "2", Content: "read https://example.com/article"},
	})
	store, cleanup := newTestStore(t)
	defer cleanup()

	type call struct{ linkID uuid.UUID; url, title string }
	var calls []call
	onVideo := func(ctx context.Context, id uuid.UUID, url, title string) error {
		calls = append(calls, call{id, url, title})
		return nil
	}

	res, err := todoistimport.Sync(context.Background(), c, store, onVideo)
	require.NoError(t, err)
	require.Equal(t, 2, res.Imported)
	require.Len(t, calls, 1)
	require.Equal(t, "https://www.youtube.com/watch?v=abcDEF12345", calls[0].url)
}

func TestTodoistSync_OnVideoIngested_NotForExistingVideo(t *testing.T) {
	url := "https://www.youtube.com/watch?v=abcDEF12345"
	store, cleanup := newTestStore(t)
	defer cleanup()
	_, _, _, err := links.IngestOne(context.Background(), store, links.Candidate{
		URL: url, Title: "v", Source: "user",
	})
	require.NoError(t, err)

	c := newFakeTodoistLister(t, []todoist.Task{{ID: "1", Content: url}})
	called := 0
	_, err = todoistimport.Sync(context.Background(), c, store, func(ctx context.Context, id uuid.UUID, url, title string) error {
		called++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 0, called)
}

func TestTodoistSync_OnVideoIngested_NilCallbackOK(t *testing.T) {
	c := newFakeTodoistLister(t, []todoist.Task{
		{ID: "1", Content: "https://www.youtube.com/watch?v=abcDEF12345"},
	})
	store, cleanup := newTestStore(t)
	defer cleanup()

	res, err := todoistimport.Sync(context.Background(), c, store, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Imported)
}
```

(Adjust helper names per the existing test file. Imports: `"context"`, `"github.com/google/uuid"`, `"darek/links"`, `"darek/tools/todoist"`.)

- [ ] **Step 2: Run, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test ./todoistimport/ -run TestTodoistSync_OnVideoIngested -v`
Expected: FAIL — compile error.

- [ ] **Step 3: Add the type and modify `Sync` + `processTask`**

In `todoistimport/sync.go`, add type:

```go
// OnVideoIngestedFunc is called once per newly-ingested video link
// (kind=="video", isNew=true). Errors are appended to Result.Errors but do
// not abort sync.
type OnVideoIngestedFunc func(ctx context.Context, linkID uuid.UUID, url, title string) error
```

Add `"github.com/google/uuid"` to imports.

Change `Sync` signature:

```go
func Sync(ctx context.Context, c Lister, store *links.Store, onVideoIngested OnVideoIngestedFunc) (*Result, error) {
```

Pass through to `processTask`:

```go
for i, t := range tasks {
	g.Go(func() error {
		outcomes[i] = processTask(gctx, c, store, t, onVideoIngested)
		return nil
	})
}
```

Modify `processTask`:

```go
func processTask(ctx context.Context, c Lister, store *links.Store, t todoist.Task, onVideoIngested OnVideoIngestedFunc) taskOutcome {
	rawURL := extractURL(t.Content, t.Description)
	if rawURL == "" {
		return taskOutcome{Skipped: true}
	}
	title := strings.TrimSpace(strings.ReplaceAll(t.Content, rawURL, ""))
	if title == "" {
		title = rawURL
	}
	id, isNew, kind, err := links.IngestOne(ctx, store, links.Candidate{
		URL:     rawURL,
		Title:   title,
		Source:  "todoist",
		Summary: links.StripHTML(t.Description),
	})
	if err != nil {
		return taskOutcome{Err: fmt.Errorf("ingest %s: %w", t.ID, err)}
	}

	if len(t.Labels) > 0 {
		tags := normalizeLabels(t.Labels)
		if len(tags) > 0 {
			if _, err := store.Pool().Exec(ctx,
				`UPDATE links SET tags = ARRAY(SELECT DISTINCT unnest(tags || $2::text[])), updated_at = now() WHERE id = $1`,
				id, tags); err != nil {
				return taskOutcome{Imported: true, Err: fmt.Errorf("merge labels %s: %w", t.ID, err)}
			}
		}
	}

	o := taskOutcome{Imported: true}
	if err := c.CompleteTask(ctx, t.ID); err != nil {
		o.Err = fmt.Errorf("complete %s: %w", t.ID, err)
	} else {
		o.Completed = true
	}

	if isNew && kind == "video" && onVideoIngested != nil {
		if cbErr := onVideoIngested(ctx, id, rawURL, title); cbErr != nil {
			if o.Err == nil {
				o.Err = fmt.Errorf("auto-analyze %s: %w", t.ID, cbErr)
			} else {
				o.Err = fmt.Errorf("%v; auto-analyze: %w", o.Err, cbErr)
			}
		}
	}
	return o
}
```

- [ ] **Step 4: Update existing todoistimport tests for the new signature**

```bash
grep -n "todoistimport.Sync\|Sync(ctx" todoistimport/sync_test.go
```

For each `Sync(ctx, c, store)` add a 4th `nil` arg.

- [ ] **Step 5: Run all todoistimport tests**

Run: `cd /Users/bklimczak/Projects/darek && go test ./todoistimport/ -v`
Expected: ALL PASS.

- [ ] **Step 6: Commit**

```bash
git add todoistimport/sync.go todoistimport/sync_test.go
git commit -m "feat(todoistimport): OnVideoIngested callback for new video rows"
```

---

## Task 7 — Shared `buildVideoAutoAnalyze` helper

**Files:**
- Create: `cmd/darek/auto_analyze.go`

A single helper builds the video-aware analyzer + the `onVideo` callback. Used by all three entry points (`serve`, standalone `freshrss sync`, standalone `todoist sync`) so the wiring lives in one place.

- [ ] **Step 1: Create the helper file**

Create `cmd/darek/auto_analyze.go`:

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"darek/analyze"
	"darek/config"
	"darek/links"
	"darek/llm"
	"darek/obs"
	"darek/tools/youtube"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// buildVideoAnalyzer constructs a VideoAwareAnalyzer wired to a real
// youtube.Client and an OpenAI-backed *Analyzer. Returns nil (and logs to
// stderr) if OpenAI is unconfigured.
func buildVideoAnalyzer(cfg *config.Config) *analyze.VideoAwareAnalyzer {
	apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
	if err != nil || apiKey == "" {
		fmt.Fprintf(os.Stderr, "info: openai not configured, video analyze disabled\n")
		return nil
	}
	llmClient, err := llm.New(llm.Options{
		APIKey:  apiKey,
		BaseURL: cfg.OpenAI.BaseURL,
		Model:   cfg.OpenAI.Model,
		Timeout: cfg.Agent.LLMTimeout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: llm client: %v (video analyze disabled)\n", err)
		return nil
	}
	ytClient := youtube.NewClient(&http.Client{Timeout: 15 * time.Second})
	return analyze.NewVideoAware(analyze.New(llmClient), ytClient)
}

// buildVideoAutoAnalyze returns a callback suitable for
// freshrssimport.OnVideoIngestedFunc / todoistimport.OnVideoIngestedFunc.
// Returns nil if `va` is nil — sync packages no-op the callback path.
//
// The returned function calls the analyzer, persists summary+tags via
// store.ApplyAnalysis, and increments darek.links.analyze with
// trigger="sync_video".
func buildVideoAutoAnalyze(va *analyze.VideoAwareAnalyzer, store *links.Store) func(ctx context.Context, id uuid.UUID, url, title string) error {
	if va == nil {
		return nil
	}
	return func(ctx context.Context, id uuid.UUID, url, title string) error {
		out, err := va.Analyze(ctx, analyze.Input{Title: title, URL: url})
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

func recordSyncAnalyze(ctx context.Context, outcome string) {
	m, _ := obs.MetricsInstance()
	if m == nil {
		return
	}
	m.LinksAnalyze.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", outcome),
		attribute.String("trigger", "sync_video"),
	))
}
```

- [ ] **Step 2: Build**

Run: `cd /Users/bklimczak/Projects/darek && go build ./...`
Expected: clean build (file is unused at this point — Go will complain about unused imports if any are wrong; otherwise OK since the package main has the funcs declared).

If `go build` complains that `buildVideoAnalyzer` / `buildVideoAutoAnalyze` are unused, that's expected — they get used in the next task. Move on.

- [ ] **Step 3: Commit**

```bash
git add cmd/darek/auto_analyze.go
git commit -m "feat(cmd/darek): buildVideoAnalyzer + buildVideoAutoAnalyze helpers"
```

---

## Task 8 — Wire helper into `serve.go`, `freshrss.go`, `todoist.go`

**Files:**
- Modify: `cmd/darek/serve.go`
- Modify: `cmd/darek/freshrss.go`
- Modify: `cmd/darek/todoist.go`

All three call sites now use the same helper. Two values per site: the `*VideoAwareAnalyzer` (used as the `serve.Analyzer` for the HTTP click path in serve only) and the `onVideo` callback (passed into Sync everywhere).

- [ ] **Step 1: Update `cmd/darek/serve.go`**

Replace the existing analyzer-building block (around lines 62-78):

```go
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
```

With:

```go
	// Build the video-aware analyzer (nil if OpenAI is unconfigured). Used
	// both as the manual-click Analyzer for the HTTP server and as the
	// engine behind the sync auto-analyze callback.
	va := buildVideoAnalyzer(&cfg)
	var analyzer serve.Analyzer
	if va != nil {
		analyzer = va
	}
	onVideo := buildVideoAutoAnalyze(va, store)
```

(Imports: drop `"darek/analyze"` if no other call sites in this file need it. The `va` variable handles both wrappers. Also drop `"darek/llm"` if its only use was here; keep it otherwise.)

Update the freshrss closure (around line 95):

```go
sync = func(ctx context.Context) (string, error) {
	res, err := freshrssimport.Sync(ctx, fr, store, onVideo)
```

Update the todoist closure (around line 113):

```go
todoistSync = func(ctx context.Context) (string, error) {
	res, err := todoistimport.Sync(ctx, td, store, onVideo)
```

- [ ] **Step 2: Update `cmd/darek/freshrss.go`**

Before the `freshrssimport.Sync(...)` call (around line 77), insert:

```go
	va := buildVideoAnalyzer(&cfg)
	onVideo := buildVideoAutoAnalyze(va, store)
```

And change the Sync call:

```go
	res, err := freshrssimport.Sync(ctx, fr, store, onVideo)
```

- [ ] **Step 3: Update `cmd/darek/todoist.go`**

Before the `todoistimport.Sync(...)` call (around line 73), insert:

```go
	va := buildVideoAnalyzer(&cfg)
	onVideo := buildVideoAutoAnalyze(va, store)
```

And change the Sync call:

```go
	res, err := todoistimport.Sync(ctx, td, store, onVideo)
```

- [ ] **Step 4: Build**

Run: `cd /Users/bklimczak/Projects/darek && go build ./...`
Expected: clean build. If there are unused-import errors, remove the now-unused imports flagged by the compiler.

- [ ] **Step 5: Run all tests**

Run: `cd /Users/bklimczak/Projects/darek && make test`
Expected: PASS.

- [ ] **Step 6: Lint**

Run: `cd /Users/bklimczak/Projects/darek && make lint`
Expected: no warnings.

- [ ] **Step 7: Commit**

```bash
git add cmd/darek/serve.go cmd/darek/freshrss.go cmd/darek/todoist.go
git commit -m "feat(cmd/darek): wire video auto-analyze in serve + freshrss + todoist syncs"
```

---

## Task 9 — README + final verification

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Update the FreshRSS / Todoist sections**

In `README.md`, find the "RSS inbox + web UI" section. After the paragraph about the analyze button, add:

```markdown

When `darek serve` (or the standalone `darek freshrss sync` / `darek todoist sync` cron commands) ingests a new YouTube video URL, it automatically fetches the transcript and runs the analyze step against the transcript instead of the YouTube description. Summary + tags are stored on the link without manual interaction. Failures (no captions, region-locked, OpenAI unavailable) are logged but don't abort sync; the row stays ingested with no `analyzed_at` set so you can click Analyze later.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs(readme): document auto-analyze for video links"
```

- [ ] **Step 3: Final full test run**

Run: `cd /Users/bklimczak/Projects/darek && make test && make lint`
Expected: all pass, no warnings.

- [ ] **Step 4: Manual smoke (optional, requires real config + YouTube + OpenAI)**

```bash
./darek serve  # in one terminal
# In another, save a YouTube URL via Todoist or FreshRSS, wait for sync.
# Open http://127.0.0.1:7777 — the new video row should already have a summary
# + tags derived from the transcript.
```

If no captions are available, the row is ingested without analyze metadata; logs (stderr) show `auto-analyze: youtube transcript: no captions available`.
