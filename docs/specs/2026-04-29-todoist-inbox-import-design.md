# Darek — Todoist #Inbox link import (design)

**Date:** 2026-04-29
**Status:** approved (awaiting implementation plan)
**Author:** brainstormed with Claude

## 1. Goal

Pull tasks from Todoist `#Inbox` that contain URLs into darek's `links` table (canonical-URL deduped, source-tagged), and complete the Todoist task once imported. Mirrors the FreshRSS source pattern: a thin orchestrator over the existing per-item `links.IngestOne` pipeline, dual-triggered via a CLI subcommand and an in-server background loop.

## 2. Scope

### In

- New `todoistimport/` package with a `Sync(ctx, client, store) (*Result, error)` orchestrator.
- `darek todoist sync` CLI subcommand (cron-friendly, one-shot).
- Second background goroutine in `darek serve` running on a separate ticker.
- New config field `todoist.sync_interval` (mirrors `freshrss.sync_interval`).
- New metric instrument `darek.todoist.sync_duration` (histogram, labels: `outcome`).
- Adds `"todoist"` to `links.normalizeSource`'s allowlist so the existing `darek.links.ingest` counter records `source="todoist"` cleanly (instead of folding into `"other"`).

### Out (deferred)

- Importing from non-`#Inbox` projects.
- Splitting a multi-URL task into multiple links.
- Reverse direction (creating Todoist tasks from rated links).
- Sync-now UI button triggering Todoist (it stays FreshRSS-only for now; revisit when there are 3+ sources).

## 3. No schema changes

The existing `links` table already supports this work. No migration. The `source` column accepts the new value `"todoist"` directly.

## 4. Per-task → link mapping

For each task returned by `client.ListTasks(ListFilter{Filter: "#Inbox"})`:

1. Find URLs in `task.Content` first; if none, in `task.Description`. Regex: `https?://[^\s<>"']+`.
2. **No URL → skip.** `Result.Skipped++`. Task stays in #Inbox, not completed.
3. **Multi-URL → first URL wins.** Documented constraint; user splits manually if they care.
4. Build a `links.Candidate`:
   - `URL`: the extracted URL.
   - `Title`: `task.Content` with the URL removed and trimmed. If the strip leaves an empty string, fall back to the URL itself (a bare-URL task; AI analyze can fill in a title-derived summary later).
   - `Summary`: `task.Description` (Todoist descriptions are plain text but piped through `links.StripHTML` as cheap insurance for any markdown/HTML the user might paste).
   - `Source`: `"todoist"`.
   - `Feed`: empty. Todoist has no feed concept, and `#Inbox` would be the same constant on every row.
   - `Kind`: empty (let `IngestOne` classify by URL).
5. Call `links.IngestOne(ctx, store, candidate)`.
6. On success:
   - `Result.Imported++`.
   - If `len(task.Labels) > 0`: direct `UPDATE links SET tags = ARRAY(SELECT DISTINCT unnest(tags || $2::text[])) WHERE id = $1` with lowercased+trimmed labels (mirrors the manual-add form's tag merge path).
   - `client.CompleteTask(task.ID)`. Failure to complete: `Result.Errors` appended; do NOT roll back the import. Task may re-import on the next run; canonical-URL upsert keeps DB clean.
7. On `IngestOne` error: leave the task in #Inbox; collect into `Result.Errors`. Don't complete.

## 5. Package layout

```
todoistimport/
├── sync.go         # Lister interface + Sync + Result + url extraction helper
└── sync_test.go    # build tag `integration`, fake Lister, 3 cases
```

### `Lister` interface

```go
type Lister interface {
    ListTasks(ctx context.Context, f todoist.ListFilter) ([]todoist.Task, error)
    CompleteTask(ctx context.Context, id string) error
}
```

`*todoist.Client` already satisfies it (existing methods).

### `Result`

```go
type Result struct {
    Imported  int
    Completed int
    Skipped   int   // tasks without a URL
    Errors    []error
}
```

### Sync algorithm

```go
func Sync(ctx context.Context, c Lister, store *links.Store) (*Result, error) {
    start := time.Now()
    res := &Result{}
    tasks, err := c.ListTasks(ctx, todoist.ListFilter{Filter: "#Inbox"})
    if err != nil {
        recordDuration(ctx, start, "error")
        return nil, fmt.Errorf("list inbox: %w", err)
    }
    for _, t := range tasks {
        rawURL := extractURL(t.Content, t.Description)
        if rawURL == "" {
            res.Skipped++
            continue
        }
        title := strings.TrimSpace(strings.ReplaceAll(t.Content, rawURL, ""))
        if title == "" { title = rawURL }
        id, _, err := links.IngestOne(ctx, store, links.Candidate{
            URL:     rawURL,
            Title:   title,
            Source:  "todoist",
            Summary: links.StripHTML(t.Description),
        })
        if err != nil {
            res.Errors = append(res.Errors, fmt.Errorf("ingest %s: %w", t.ID, err))
            continue
        }
        res.Imported++
        if len(t.Labels) > 0 {
            tags := normalizeLabels(t.Labels)
            if len(tags) > 0 {
                _, _ = store.Pool().Exec(ctx,
                    `UPDATE links SET tags = ARRAY(SELECT DISTINCT unnest(tags || $2::text[])), updated_at = now() WHERE id = $1`,
                    id, tags)
            }
        }
        if err := c.CompleteTask(ctx, t.ID); err != nil {
            res.Errors = append(res.Errors, fmt.Errorf("complete %s: %w", t.ID, err))
            continue
        }
        res.Completed++
    }
    outcome := "ok"
    if len(res.Errors) > 0 { outcome = "partial" }
    recordDuration(ctx, start, outcome)
    return res, nil
}
```

### `extractURL` helper

```go
var urlRE = regexp.MustCompile(`https?://[^\s<>"']+`)

func extractURL(content, description string) string {
    if m := urlRE.FindString(content); m != "" {
        return m
    }
    return urlRE.FindString(description)
}
```

## 6. CLI subcommand

New file `cmd/darek/todoist.go` mirroring `cmd/darek/freshrss.go`:

```go
func runTodoist(ctx context.Context, cfgPath string, args []string) error {
    if len(args) == 0 {
        return fmt.Errorf("usage: darek todoist sync")
    }
    switch args[0] {
    case "sync":
        return runTodoistSync(ctx, cfgPath)
    default:
        return fmt.Errorf("unknown todoist subcommand %q (try: sync)", args[0])
    }
}
```

`runTodoistSync` opens DB + obs, builds the Todoist client (`todoist.New(Options{Token})`), runs `todoistimport.Sync`, prints a summary line, returns non-zero if errors.

`cmd/darek/main.go` switch gains:

```go
case "todoist":
    return runTodoist(ctx, cfgPath, args)
```

Default error message updated to mention `todoist`.

## 7. In-server loop

`cmd/darek/serve.go`'s `runServe` adds a parallel Todoist sync goroutine alongside the existing FreshRSS one.

After the existing FreshRSS sync setup, add:

```go
var todoistSync serve.SyncFn
if cfg.Todoist.TokenEnv != "" {
    token, err := config.ResolveSecret("env:" + cfg.Todoist.TokenEnv)
    if err == nil && token != "" {
        td, err := todoist.New(todoist.Options{Token: token})
        if err == nil {
            todoistSync = func(ctx context.Context) (string, error) {
                res, err := todoistimport.Sync(ctx, td, store)
                if err != nil { return "", err }
                return fmt.Sprintf("imported=%d completed=%d skipped=%d errors=%d",
                    res.Imported, res.Completed, res.Skipped, len(res.Errors)), nil
            }
        } else {
            fmt.Fprintf(os.Stderr, "warn: todoist client: %v\n", err)
        }
    }
}
```

After the existing `runSyncLoop(...)` for FreshRSS:

```go
if todoistSync != nil && cfg.Todoist.SyncInterval > 0 {
    go runSyncLoop(ctx, todoistSync, cfg.Todoist.SyncInterval, "todoist")
}
```

`runSyncLoop` gains a `name string` parameter so its stderr log lines disambiguate (`"todoist sync: ..."` vs `"freshrss sync: ..."`). The existing FreshRSS callsite passes `"freshrss"`.

## 8. Config

`config/types.go`:

```go
type Todoist struct {
    TokenEnv     string        `yaml:"token_env"`
    SyncInterval time.Duration `yaml:"sync_interval"`
}
```

`config/testdata/config.example.yaml`:

```yaml
todoist:
  token_env: DAREK_TODOIST_TOKEN
  sync_interval: 15m       # how often the in-server loop polls (0 disables)
```

## 9. Metrics

New instrument in `obs/metrics.go`:

```go
TodoistSyncDuration metric.Float64Histogram
```

OTel name `darek.todoist.sync_duration`, unit `s`. Labels: `outcome ∈ {ok, partial, error}`.

`obs/metrics_test.go` extended with `require.NotNil(t, m.TodoistSyncDuration, ...)`.

`links.normalizeSource` gains `"todoist"` to its allowlist:

```go
func normalizeSource(s string) string {
    switch s {
    case "freshrss", "user", "email", "todoist":
        return s
    default:
        return "other"
    }
}
```

The existing cardinality test (`obs/cardinality_test.go`) pins allowed `dep`/`op` pairs for `darek.dep.*` only. Todoist's `list_tasks` and `complete_task` are already in the allowlist. No change needed.

## 10. Testing

### `todoistimport/sync_test.go` (build tag `integration`)

Fake `Lister` with three cases:

- **Mixed inbox**: 3 tasks — one with a URL in content, one with no URL, one with a URL only in description. Assert: 2 imported (and completed), 1 skipped (and not completed). DB has 2 rows.
- **Labels merge**: a task with labels `["Go", "concurrency"]` becomes a link with those tags lowercased. Existing user-tag survives the merge.
- **IngestOne error path**: a task with `URL: "not a url"` → ingestion fails, task is NOT completed, error collected.

### `links/ingest_test.go` extension

One additional case: `Candidate{Source: "todoist"}` results in stored row with `source="todoist"` (sanity check that the `normalizeSource` allowlist change doesn't change behavior).

### Manual verification

`darek todoist sync` once #Inbox has a few link-tasks; observe one task completes and one corresponding row in the links UI. Re-run; no duplicates (canonical URL upsert).

## 11. Risks / migration

- **No DB migration.** Additive only.
- **Concurrent FreshRSS + Todoist syncs:** both write through `links.IngestOne`, idempotent on canonical URL. Safe.
- **CompleteTask after a re-run on already-completed task:** Todoist returns 4xx; we collect into `Result.Errors` and move on. Idempotent in practice because a completed task no longer appears in `#Inbox` filter results, so the re-run window is small.
- **Multi-URL tasks lose extra URLs.** Documented constraint.
- **Description-only URLs** (URL only in description, not content): handled by the fallback regex pass.

## 12. Out of scope

Recapped from §2:

- Importing from non-`#Inbox` projects.
- Splitting multi-URL tasks.
- Reverse direction (links → Todoist tasks).
- Sync-now UI button covering Todoist.
