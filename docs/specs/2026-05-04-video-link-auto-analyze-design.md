# Darek — Auto-analyze video links from RSS / Todoist (design)

**Date:** 2026-05-04
**Status:** approved (awaiting implementation plan)
**Author:** brainstormed with Claude

## 1. Goal

When a YouTube video URL is newly ingested via FreshRSS or Todoist sync, automatically fetch its transcript and run the existing `analyze` flow against the transcript instead of the source-provided description, persisting summary + tags. The manual "Analyze" button on a video row in the inbox UI also switches to using the transcript, so click-to-analyze produces the same quality result as auto-analyze.

## 2. Scope

### In

- New `analyze.VideoAwareAnalyzer` wrapper that detects YouTube URLs, fetches the transcript via `*tools/youtube.Client`, replaces `Input.Body` with the transcript text, and delegates to the existing `*Analyzer`.
- New `*links.Store.ApplyAnalysis(ctx, id, summary, tags)` method extracting the existing analyze-handler SQL into a reusable place.
- Optional `OnVideoIngested` callback added to `freshrssimport.Sync` and `todoistimport.Sync`. Callback is invoked when `IngestOne` returns `isNew=true && kind=="video"` and the callback is non-nil.
- Wiring in `cmd/darek/serve.go`, `cmd/darek/freshrss.go`, `cmd/darek/todoist.go` to construct a `VideoAwareAnalyzer` and an `OnVideoIngested` closure that runs analyze + persists.
- Existing `serve.Server` swaps `*analyze.Analyzer` for `*analyze.VideoAwareAnalyzer` so manual click also uses transcripts.
- New `trigger` label on the `darek.links.analyze` counter (`"manual"` | `"sync_video"`), added to the cardinality allowlist.

### Out (deferred)

- Backfilling existing un-analyzed video rows. Only newly-ingested videos auto-analyze; pre-existing rows are reachable via the manual button.
- Persisting the raw transcript on the link row. Transcript stays in-memory during analyze; only summary + tags are stored.
- Concurrency / batching for sync auto-analyze. Items process sequentially; a sync run with N new videos blocks for N × (transcript fetch + OpenAI call).
- Non-YouTube video sources (Vimeo, TikTok). The `kind=="video"` classifier matches them, but the transcript fetcher only handles YouTube; the wrapper checks `youtube.ExtractVideoID` and skips other video hosts.
- Per-user `lang` preference for transcript selection. The default fallback (manual-en → auto-en → first) is used.

### Dependency

This design assumes [`docs/plans/2026-05-04-youtube-transcript-tool.md`](../plans/2026-05-04-youtube-transcript-tool.md) has landed. It reuses `youtube.Client.Fetch` and `youtube.ExtractVideoID` from that plan. The implementation order is: previous plan first, then this one.

## 3. Architecture

Two layers:

**Layer A — `*analyze.VideoAwareAnalyzer`.** Composes the existing `*Analyzer` with a `Transcriber` interface. Detects YouTube URLs, fetches transcript, swaps `Input.Body`, delegates. Drop-in replacement for callers that hold an `*Analyzer`.

**Layer B — sync `OnVideoIngested` callback.** New optional field on `freshrssimport.SyncOptions` and `todoistimport.SyncOptions`. Wiring code provides a closure that runs `VideoAwareAnalyzer.Analyze` + `Store.ApplyAnalysis`. Sync packages stay free of analyze/youtube imports.

Failure handling:

- Transcript fetch error → propagated up (no silent fallback to YT description). Sync logs + counter increments + continues to next item.
- Analyze error → same.
- Manual-click error → existing inline error rendering already handles this; new errors come through the same path.

## 4. `analyze` package changes

### `analyze/video_aware.go` (new file)

```go
package analyze

import (
    "context"
    "fmt"

    "darek/tools/youtube"
)

// Transcriber is the subset of *youtube.Client used by VideoAwareAnalyzer.
type Transcriber interface {
    Fetch(ctx context.Context, rawURL, lang string) (youtube.Result, error)
}

// VideoAwareAnalyzer wraps *Analyzer. For YouTube URLs it fetches the
// transcript and uses it as Input.Body, ignoring whatever Body the caller
// passed. For non-YouTube URLs it delegates straight to the inner Analyzer.
type VideoAwareAnalyzer struct {
    inner       *Analyzer
    transcriber Transcriber
}

func NewVideoAware(inner *Analyzer, t Transcriber) *VideoAwareAnalyzer {
    return &VideoAwareAnalyzer{inner: inner, transcriber: t}
}

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

### `analyze/video_aware_test.go` (new file)

Cases:

- **Happy YouTube URL:** stub `Transcriber` returns `youtube.Result{Text: "transcript body"}`. Stub `Chat` (reused from existing analyze tests) returns valid JSON. Assert the `Body` field of the user message reaching `Chat` is `"transcript body"`, NOT whatever was passed in `Input.Body`.
- **Non-YouTube URL:** `Input.URL = "https://example.com/article"`. Recording stub `Transcriber` records calls — assert it was never invoked. `Input.Body` reaches the inner analyzer unchanged.
- **Transcript fetch fails:** `Transcriber` returns error. Wrapper returns `"youtube transcript: <err>"` and Chat is never called.

The existing `*Analyzer` is unchanged. `VideoAwareAnalyzer.Analyze` has the same signature, so it's a drop-in for callers.

## 5. `links` package changes

### `*Store.ApplyAnalysis` (new method)

Extracted from the SQL currently inlined in `serve.handleAnalyze`:

```go
func (s *Store) ApplyAnalysis(ctx context.Context, id uuid.UUID, summary string, tags []string) error {
    _, err := s.pool.Exec(ctx, `
        UPDATE links
           SET summary     = $2,
               tags        = ARRAY(SELECT DISTINCT unnest(tags || $3::text[])),
               analyzed_at = now(),
               updated_at  = now()
         WHERE id = $1
    `, id, summary, tags)
    return err
}
```

Takes primitive `summary string, tags []string` (not `analyze.Output`) to avoid a `links → analyze` import. Callers unpack `Output` at the call site.

### `serve.handleAnalyze` (modify)

Replace the inline `s.store.Pool().Exec(...)` with `s.store.ApplyAnalysis(...)`. No behavior change.

### Tests

`links/store_test.go` gains a test: `ApplyAnalysis` sets `summary`, merges new tags into existing tags (dedupe), and sets `analyzed_at`.

## 6. `freshrssimport` and `todoistimport` changes

Both packages currently call `Sync(ctx, opts)` where `opts` is a struct of dependencies. Add a single optional field:

```go
// freshrssimport.SyncOptions
type SyncOptions struct {
    // …existing fields…

    // OnVideoIngested, if non-nil, is invoked once per newly-ingested video
    // link (kind=="video", isNew=true). Errors are logged + counted but do
    // not abort the sync — the row stays ingested without analyze metadata.
    OnVideoIngested func(ctx context.Context, linkID uuid.UUID, url, title string) error
}
```

Same field added to `todoistimport.SyncOptions`.

`links.IngestOne` signature change: returns `(uuid.UUID, bool, string, error)` where the third value is the resolved `kind`. Cleaner than re-classifying inside the sync loop. Two internal callers (freshrssimport, todoistimport) plus existing tests update mechanically.

In each `Sync` loop, after the existing `IngestOne` call:

```go
id, isNew, kind, err := links.IngestOne(ctx, store, c)
if err != nil {
    // existing error handling
}
if isNew && kind == "video" && opts.OnVideoIngested != nil {
    if err := opts.OnVideoIngested(ctx, id, c.URL, c.Title); err != nil {
        opts.Logger.Warn("video auto-analyze failed",
            "url", c.URL, "err", err)
    }
}
```

Tests in `links/ingest_test.go` update for the new return value (mechanical).

### Sync test additions

For both `freshrssimport/sync_test.go` and `todoistimport/sync_test.go`:

- Recording callback: ingest a fixture batch with mixed article + video URLs; assert callback fires exactly once per new video, with the correct `(id, url, title)`.
- Already-existing video (second sync run): callback does NOT fire.
- Non-video URL: callback does NOT fire.
- Callback returns an error: sync continues, returned `Result` reflects the same number of ingested rows.

## 7. Wiring (`cmd/darek/`)

### `cmd/darek/serve.go`

Currently builds `analyzer := analyze.New(llmClient)`. Change to:

```go
ytClient := youtube.NewClient(&http.Client{Timeout: 15 * time.Second})
videoAnalyzer := analyze.NewVideoAware(analyzer, ytClient)

srv, err := serve.New(store, sync, videoAnalyzer)
```

`serve.Server`'s analyzer field type changes from `*analyze.Analyzer` to `*analyze.VideoAwareAnalyzer`. Same `Analyze` method, same call site in `handleAnalyze`, no other changes.

The `OnVideoIngested` closure used for periodic sync inside `serve` (and the same closure pattern below):

```go
m, _ := obs.MetricsInstance()

onVideo := func(ctx context.Context, id uuid.UUID, url, title string) error {
    out, err := videoAnalyzer.Analyze(ctx, analyze.Input{Title: title, URL: url})
    if err != nil {
        if m != nil {
            m.LinksAnalyze.Add(ctx, 1, metric.WithAttributes(
                attribute.String("outcome", "error"),
                attribute.String("trigger", "sync_video"),
            ))
        }
        return err
    }
    if err := store.ApplyAnalysis(ctx, id, out.Summary, out.Tags); err != nil {
        if m != nil {
            m.LinksAnalyze.Add(ctx, 1, metric.WithAttributes(
                attribute.String("outcome", "error"),
                attribute.String("trigger", "sync_video"),
            ))
        }
        return err
    }
    if m != nil {
        m.LinksAnalyze.Add(ctx, 1, metric.WithAttributes(
            attribute.String("outcome", "ok"),
            attribute.String("trigger", "sync_video"),
        ))
    }
    return nil
}
```

If `videoAnalyzer == nil` (no OpenAI key), `onVideo = nil` — sync skips the callback path and behavior matches today.

Pass `onVideo` into `freshrssimport.SyncOptions.OnVideoIngested` and `todoistimport.SyncOptions.OnVideoIngested` everywhere those structs are built (the periodic sync loops in `serve` plus the standalone CLI runs).

### `cmd/darek/freshrss.go` and `cmd/darek/todoist.go`

Cron-driven `darek freshrss sync` and `darek todoist sync`. Same wiring as above — build the analyzer + transcriber + closure, pass into the sync options. If OpenAI is unconfigured, log a warning and pass `OnVideoIngested: nil`.

## 8. Observability

Existing `darek.links.analyze` counter gains a new label:

```go
LinksAnalyze metric.Int64Counter // labels: outcome, trigger
```

`trigger` ∈ {`"manual"`, `"sync_video"`}.

Update the cardinality test allowlist (`obs/metrics_test.go` or wherever the allowlist lives) to include the new label key and its two values.

The transcript fetch and the OpenAI call are inside `VideoAwareAnalyzer.Analyze`; existing instruments cover both:

- Transcript HTTP requests are wrapped by Go's default client — for v1, no per-dep instrument. (Already deferred from the youtube agent-tool design.)
- OpenAI tokens / cost / latency: existing `darek.tokens.*`, `darek.llm.cost_usd`, `darek.llm.latency`, `darek.dep.*` counters via `*llm.Client.Chat`.

## 9. Risks

- **Sync latency.** A FreshRSS sync that ingests 10 new videos now adds ~10 × (1-3s transcript fetch + 2-5s OpenAI) = 30-80s. Acceptable for cron-driven sync; if it bites, switch to a worker queue (out of scope).
- **Cost.** Each new video costs the same ~$0.01 as a manual click (transcript content stays under the existing 6000-char `maxBodyChars` cap). At 50 new videos per week, a few dollars per month. Easy to forecast; easy to disable by removing `OnVideoIngested`.
- **Videos without captions.** No-captions error from the transcriber bubbles up; the row is left without `analyzed_at`. User notices and can either accept it or click Analyze later (which re-surfaces the same error).
- **Non-YouTube videos (Vimeo, TikTok, etc.).** `youtube.ExtractVideoID` returns an error for these, so `VideoAwareAnalyzer` skips transcript fetch and falls through to inner `Analyzer.Analyze` with the empty Body — analyze runs from title + URL. Sync still fires `OnVideoIngested` for them (kind=="video"). The result is a title-only summary, same as today's behavior for caption-less videos. Acceptable for v1.
- **`ApplyAnalysis` race.** Two simultaneous analyze runs on the same id (sync auto + user clicks while sync is running) both UPDATE the row. Last writer wins. The existing manual-click handler has the same race. Acceptable.

## 10. Out of scope

Recapped from §2:

- Backfill of existing un-analyzed videos.
- Persisted transcript column.
- Sync-side concurrency.
- Non-YouTube transcript sources.
- Per-user `lang` preference.
