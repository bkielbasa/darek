# Darek — RSS inbox + link triage UI (design)

**Date:** 2026-04-29
**Status:** approved (awaiting implementation plan)
**Author:** brainstormed with Claude

## 1. Goal

Pull articles, videos, podcasts, and posts from FreshRSS into darek's local link store, mark them read in FreshRSS, and serve a small HTMX-driven web UI for browsing and rating the queue. The same per-item ingestion pipeline should accept future sources (email-extracted URLs, anything else) without rewriting.

## 2. Scope

### In

- Periodic ingestion of unread FreshRSS articles into the existing `links` table.
- URL canonicalization to dedupe across sources (RSS, manual, future email).
- A `kind` field on every link (`article|video|tweet|podcast|other`), set by URL classifier at import.
- A `feed` field on RSS-imported links for filtering.
- Two sync triggers: `darek freshrss sync` CLI subcommand (cron-friendly, mirrors `darek mail sync`), and an in-server background loop while `darek serve` is running.
- HTTP server (`darek serve`) — server-rendered HTML + HTMX. Hybrid view: queue (unrated) by default, archive (all) one click away. Inline edits for rating, tags, notes, kind.
- Source-agnostic per-item pipeline (`links.IngestOne`) so future sources are thin orchestrators.

### Out (deferred)

- Multi-user / auth — bind to `127.0.0.1`, done.
- TLS — local-only.
- Server-side render of article content. Click-through to source.
- Comment threads — single `notes` block.
- WebSocket / live updates between clients.
- Email-derived link source — the design accommodates it; implementation lands later.

## 3. Schema

Migration `db/migrations/0004_links_rss.up.sql`:

```sql
ALTER TABLE links ADD COLUMN kind text NOT NULL DEFAULT 'article';
ALTER TABLE links ADD COLUMN feed text;
ALTER TABLE links ADD CONSTRAINT links_kind_check
    CHECK (kind IN ('article','video','tweet','podcast','other'));
CREATE INDEX links_kind ON links (kind);
CREATE INDEX links_feed ON links (feed) WHERE feed IS NOT NULL;
```

- `kind` defaults to `article`. Set by classifier at import. User-overridable in UI.
- `feed` is the FreshRSS feed name from `Article.Feed`. Null for non-RSS sources.
- `source` (existing): `freshrss` for RSS imports, `user` for manual additions, future `email` etc.
- **No `read_at` / `status` column.** Queue view is `WHERE rating IS NULL`, archive view is no filter.
- The existing tsvector `search` column already covers title/notes/tags/url and updates on changes.

## 4. Per-item pipeline (source-agnostic)

`links/ingest.go`:

```go
type Candidate struct {
    URL    string  // raw URL from the source
    Title  string  // optional
    Source string  // "freshrss" | "email" | "user" | …
    Feed   string  // optional, e.g. RSS feed name or email From-header
    Kind   string  // optional override; classifier runs if empty
}

// IngestOne canonicalizes, classifies, and upserts into links.
// Returns the resulting link id and whether it was a new row.
func IngestOne(ctx context.Context, store *links.Store, c Candidate) (id uuid.UUID, isNew bool, err error)
```

This is the only place that knows about canonicalization + classification + the upsert. Every future source produces `Candidate`s and hands them to `IngestOne`.

### URL canonicalization (`links.Canonicalize`)

Pure function. Steps:

1. Lowercase scheme + host. Drop `www.` prefix on host.
2. Strip a closed allowlist of tracking params: `utm_*`, `fbclid`, `gclid`, `mc_eid`, `mc_cid`, `_hsenc`, `_hsmi`, `ref`, `ref_src`, `ref_url`, `source`, `igshid`, `share`, `share_id`, `feature`. The YouTube `si` param strips on `kind=video` URLs only; `t` (timestamp) is preserved everywhere (it's a real deep-link offset for video/podcast).
3. Drop trailing slash unless path is just `/`.
4. Drop fragments (`#...`). Hosts that genuinely use fragment routing (Twitter/X, etc.) are listed in a small allowlist that preserves them.
5. Sort remaining query params alphabetically.

The result is the value used as the unique key on `links.url`. Unit-tested against a fixture table of input → expected output pairs (utm strip, www strip, fragment drop, ordering, etc.).

### Kind classifier (`links.Classify`)

URL-host pattern match in order:

- `video`: `youtube.com`, `youtu.be`, `vimeo.com`, `tiktok.com`
- `tweet`: `twitter.com`, `x.com`, `mastodon.*` hosts, `bsky.app`
- `podcast`: `anchor.fm`, `podcasts.apple.com`, `open.spotify.com/episode`, `overcast.fm`, hosts ending in `.libsyn.com`, URL ending in `.mp3`/`.m4a`
- `article`: default

Patterns live in a single map at the top of `links/classify.go`; adding hosts is a one-liner.

## 5. Sync orchestration (RSS source)

New package `freshrssimport` (peer of `links/`, `memory/`).

```go
func Sync(ctx context.Context, fr *freshrss.Client, store *links.Store) (*Result, error)
```

Algorithm:

1. List unread articles via `fr.List({Filter: FilterUnread, Limit: 1000})`. Paginate via FreshRSS continuation token if needed.
2. For each article:
   - Build `Candidate{Source: "freshrss", URL: art.URL, Title: art.Title, Feed: art.Feed}`.
   - Call `links.IngestOne(ctx, store, candidate)`.
   - On success, queue the article id for mark-as-read.
3. After all articles processed, `fr.Mark(id, ActionMarkRead)` for each successful id. Failures logged + counted but don't fail the whole sync.
4. Return `Result{Imported, MarkedRead, Skipped, Errors}`.

### Sync triggers (both)

- **CLI**: `darek freshrss sync` — one-shot, exits. Cron-friendly. Mirrors `darek mail sync`.
- **In-server loop**: `darek serve` spawns a goroutine that runs `Sync` immediately on startup, then on a `time.Ticker(freshrss.sync_interval)`. Default interval `15m`, configurable in `~/.darek/config.yaml`. Setting interval to `0` disables the in-server loop.

Both triggers call the same `freshrssimport.Sync` — no logic duplication. Concurrent runs (cron + in-server) are tolerated: the local upsert is idempotent on canonical URL, and `fr.Mark` is idempotent.

## 6. HTTP server

New CLI subcommand: `darek serve`. Long-running. Same config file.

```yaml
server:
  bind: 127.0.0.1:7777   # default, local-only

freshrss:
  sync_interval: 15m     # 0 disables in-server loop; cron path still works
```

`darek serve` opens the DB + obs, starts the HTTP server, and spawns the sync goroutine. Both goroutines share a context so `Ctrl+C` shuts both down cleanly.

### Routes

| Method | Path | Purpose | Returns |
|---|---|---|---|
| GET  | `/`                                                | Queue view (`rating IS NULL`) — default home | full page |
| GET  | `/all`                                             | Archive view (everything) | full page |
| GET  | `/links/{id}`                                      | Detail view | full page |
| POST | `/links/{id}/rating` (form: `value=N`, 0 unsets)   | Set rating | rating widget swap |
| POST | `/links/{id}/tags` (form: `tag=...`, `op=add\|remove`) | Add/remove tag | tags row swap |
| POST | `/links/{id}/notes` (form: `notes=...`)            | Save notes | notes block swap |
| POST | `/links/{id}/kind` (form: `kind=...`)              | Override kind | kind badge swap |
| POST | `/links/new` (form: `url=...`, `notes=...`, `tags=...`) | Manual add | redirect to `/` |
| POST | `/sync`                                            | Trigger immediate sync ("Sync now" button) | status banner swap |
| GET  | `/healthz`                                         | Liveness | 200 |

Both list views accept query params: `?kind=video&feed=lobsters&q=concurrency&min_rating=4`. Filter chips on the page set these. The list is a single HTMX target so chip clicks swap just the list, not the whole page.

### List row shape (inline-edit)

```
[★★★☆☆] [video] [Hacker News] How to Write a Bootloader     · 2h ago     [tags: kernel, low-level (+)]
                                                                          [notes: …expand]
```

- Stars: 5 buttons; click sets rating; click current rating again unsets.
- Kind badge: small colored pill; click opens dropdown.
- Feed name: filter link (sets `?feed=…` and re-fetches list).
- Tags: chip list with `(+)` to add, `(×)` per chip to remove. All HTMX.
- Notes: collapsed one-liner; click expands inline editor.
- Title links out (`target="_blank" rel="noopener"`) — that's the "click to navigate to article" path.

Unrated rows in the queue stay until rated; queue shrinks naturally.

### Templates

Under `cmd/darek/serve/templates/` (embedded via `embed.FS`):

- `layout.html` — shell, HTMX from CDN as the only `<script>`.
- `index.html` — list page, used by both `/` and `/all`.
- `_row.html` — single link row partial (HTMX swap target).
- `_rating.html`, `_tags.html`, `_notes.html`, `_kind.html` — fragments for inline edits.
- `detail.html` — full detail view (the row + a larger notes editor).

A small `style.css` served from the same embedded FS. No custom JS.

### Concurrency vs other entry points

- The agent's `links.save` / `links.search` / `links.similar` tools keep working unchanged — they call `links.Store` directly. They never talk to the HTTP server.
- `darek freshrss sync` (cron) and the in-server loop both call `freshrssimport.Sync` — Postgres handles concurrent writers; FreshRSS `mark` is idempotent.

## 7. Observability

- `obs.Dep("freshrss", op, ...)` already wraps API calls; sync inherits it.
- New counter `darek.links.ingest` (labels: `source` ∈ enum, `kind` ∈ enum, `outcome` ∈ {ok,error}) — bumped from `links.IngestOne`. Kept separate from the existing `darek.links.events` counter (which has only an `op` label) so label shapes don't mix.
- New histogram `darek.freshrss.sync_duration` (labels: `outcome`).
- The cardinality test allowlist gains entries for the new `source` and `kind` enums.
- HTTP server exports a single OTel HTTP server middleware around the mux for span coverage. No new metric instruments — request counts come from the HTTP middleware's standard metrics.

## 8. Testing

- `links.Canonicalize` — fixture-table unit test (~20 cases covering each rule).
- `links.Classify` — fixture-table unit test for each kind branch.
- `links.IngestOne` — integration test (testcontainers Postgres): canonical URL collisions, source labeling, classifier integration, idempotent re-ingest.
- `freshrssimport.Sync` — integration test with a fake `freshrss.Client` (or HTTP mux mock): drives a list of mock articles, asserts each gets imported and `Mark` is called.
- HTTP handlers — `httptest.NewRecorder` + golden-file responses for the partial swaps. Smoke test that POST `/links/{id}/rating` updates the row in DB and returns the updated row HTML.
- Manual verification: `darek serve`, browse queue, rate a few items, confirm cron `darek freshrss sync` ingests new items between sessions.

## 9. Risks / migration

- The `0004` migration is additive (`ALTER TABLE … ADD COLUMN`) and safe to run on existing rows. Existing rows get `kind='article'` and `feed=NULL` defaults.
- Existing `links.Save` callers (chat tools, manual UI add later) need to route through `links.IngestOne` to get canonicalization. The agent's `links.save` tool's `Execute` is updated to do this; behavior is unchanged from the agent's perspective beyond the canonicalized URL key.
- URL canonicalization could collide two pre-existing rows (e.g. one with `?utm_source=foo`, one without) on first run. Migration data path: leave existing rows alone (they were saved with un-canonicalized URLs); only new ingests via `IngestOne` get canonical. Future cleanup script can normalize old rows if desired — out of scope.
- The HTMX CDN dependency is the only runtime resource the page can't serve offline. Acceptable for a single-user laptop tool. If offline is needed later, ship HTMX as a static asset in the embedded FS.

## 10. Out of scope

Recapped from §2 for clarity:

- Multi-user / auth.
- TLS.
- Server-side article rendering.
- Comment threads (single notes block).
- WebSocket / live cross-tab updates.
- Email-extracted links (planned, not in this plan).
