package blogmarketing

import (
	"context"
	"fmt"
	"time"

	"darek/exechistory"
	"darek/obs"
	"darek/tools/blogfeed"
	"darek/tools/todoist"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("darek/blogmarketing")

// FeedLister is the subset of *blogfeed.Client used by Sync.
type FeedLister interface {
	List(ctx context.Context) ([]blogfeed.Entry, error)
}

// TodoistAPI is the subset of *todoist.Client used by Sync and the reverse-
// sync helpers in this package. Defined here so tests can supply a fake.
type TodoistAPI interface {
	ResolveProjectID(ctx context.Context, name string) (string, error)
	CreateTask(ctx context.Context, req todoist.CreateRequest) (*todoist.Task, error)
	DeleteTask(ctx context.Context, id string) error
	GetTask(ctx context.Context, id string) (*todoist.Task, error)
}

// Config is the per-run configuration. PostTime is "HH:MM"; Timezone is the
// location the schedule is anchored in (system local if nil).
type Config struct {
	FeedURL      string
	ProjectName  string
	PostTime     string
	Timezone     *time.Location
	SyncInterval time.Duration
}

// Result is the aggregated outcome of one Sync call.
type Result struct {
	Scheduled    int     // entries that got the full 9-task series this run
	BackfillSeen int     // entries inserted as seen-only on the first-ever run
	Skipped      int     // entries already in the state table
	Errors       []error // per-entry errors; do not abort sibling entries
}

// Sync polls the feed, schedules new posts, and returns aggregated counts.
func Sync(ctx context.Context, feed FeedLister, store *Store, drafter Drafter, td TodoistAPI, cfg Config) (*Result, error) {
	ctx, span := tracer.Start(ctx, "blogmarketing.sync")
	exechistory.MarkExecution(span, "blog-marketing-sync")
	defer span.End()

	start := time.Now()
	res := &Result{}
	outcomeStr := "error"
	defer func() { recordDuration(ctx, start, outcomeStr) }()

	entries, err := feed.List(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("feed list: %w", err)
	}

	count, err := store.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("store count: %w", err)
	}
	firstRun := count == 0

	loc := cfg.Timezone
	if loc == nil {
		loc = time.Local
	}
	hh, mm, err := parsePostTime(cfg.PostTime)
	if err != nil {
		return nil, fmt.Errorf("post_time: %w", err)
	}

	// Resolve project ID once per poll, lazily — only when actually needed.
	var projectID string
	resolveProject := func() (string, error) {
		if projectID != "" {
			return projectID, nil
		}
		id, err := td.ResolveProjectID(ctx, cfg.ProjectName)
		if err != nil {
			return "", err
		}
		projectID = id
		return id, nil
	}

	for _, e := range entries {
		if e.CanonicalURL == "" {
			res.Skipped++
			continue
		}
		seen, err := store.IsScheduled(ctx, e.CanonicalURL)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: is_scheduled: %w", e.CanonicalURL, err))
			continue
		}
		if seen {
			res.Skipped++
			continue
		}
		if firstRun {
			if err := store.MarkSeenOnly(ctx, e.CanonicalURL, e.PublishedAt); err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: mark_seen_only: %w", e.CanonicalURL, err))
				continue
			}
			res.BackfillSeen++
			continue
		}

		pid, err := resolveProject()
		if err != nil {
			// Project resolution is required for ALL entries; fail hard so a typo
			// in project_name is surfaced immediately, not silently amortized.
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return res, fmt.Errorf("resolve project %q: %w", cfg.ProjectName, err)
		}

		drafts, err := drafter.Draft(ctx, e)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: draft: %w", e.CanonicalURL, err))
			continue
		}

		launchDate := launchDateFor(e.PublishedAt, time.Now(), loc)
		dueByCadence := map[Cadence]time.Time{
			CadenceLaunch:       atTime(launchDate, hh, mm, loc),
			CadenceReshare2W:    atTime(launchDate.AddDate(0, 0, 14), hh, mm, loc),
			CadenceResurface3Mo: atTime(launchDate.AddDate(0, 0, 90), hh, mm, loc),
		}

		refs, err := createSeries(ctx, td, pid, e, drafts, dueByCadence)
		if err != nil {
			rollback(ctx, td, refs)
			res.Errors = append(res.Errors, fmt.Errorf("%s: create series: %w", e.CanonicalURL, err))
			continue
		}
		if err := store.SaveTasks(ctx, e.CanonicalURL, e.PublishedAt, refs); err != nil {
			// State write failed AFTER all 9 tasks landed — log but DON'T roll back
			// (would delete real Todoist tasks the user can see); next poll will
			// re-detect via the feed but IsScheduled will return false, so it will
			// re-create. Documented as a known edge in the spec's "open considerations".
			res.Errors = append(res.Errors, fmt.Errorf("%s: save_tasks: %w", e.CanonicalURL, err))
			continue
		}
		res.Scheduled++
	}

	span.SetAttributes(
		attribute.Int("scheduled", res.Scheduled),
		attribute.Int("backfill_seen", res.BackfillSeen),
		attribute.Int("skipped", res.Skipped),
		attribute.Int("errors", len(res.Errors)),
	)
	outcomeStr = outcome(res)
	return res, nil
}

// createSeries creates the 9 tasks sequentially. Returns the refs created so
// far and the first error encountered. Each ref carries its (platform, cadence)
// tag so the caller can persist a normalised mapping without re-deriving by
// list position.
func createSeries(ctx context.Context, td TodoistAPI, projectID string, e blogfeed.Entry, drafts Drafts, due map[Cadence]time.Time) ([]TaskRef, error) {
	refs := make([]TaskRef, 0, 9)
	for _, p := range AllPlatforms {
		for _, c := range AllCadences {
			req := todoist.CreateRequest{
				Content:     drafts[p][c],
				Description: e.URL,
				ProjectID:   projectID,
				Labels:      []string{string(p), string(c)},
				DueDatetime: due[c].Format(time.RFC3339),
			}
			t, err := td.CreateTask(ctx, req)
			if err != nil {
				return refs, err
			}
			refs = append(refs, TaskRef{Platform: p, Cadence: c, TodoistID: t.ID})
		}
	}
	return refs, nil
}

// rollback best-effort deletes already-created tasks. Errors are recorded as
// span events and dropped — partial-failure recovery does not block retry.
func rollback(ctx context.Context, td TodoistAPI, refs []TaskRef) {
	span := trace.SpanFromContext(ctx) // never nil; no-op if no active span
	for _, r := range refs {
		if err := td.DeleteTask(ctx, r.TodoistID); err != nil {
			span.AddEvent("rollback delete failed", trace.WithAttributes(
				attribute.String("task_id", r.TodoistID),
				attribute.String("error", err.Error()),
			))
		}
	}
}

// launchDateFor returns the calendar date in loc that is the later of
// e.PublishedAt and now (truncated to the start of that day in loc).
func launchDateFor(publishedAt, now time.Time, loc *time.Location) time.Time {
	candidate := publishedAt
	if now.After(candidate) {
		candidate = now
	}
	c := candidate.In(loc)
	return time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, loc)
}

func atTime(day time.Time, hh, mm int, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, loc)
}

func parsePostTime(s string) (int, int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("expected HH:MM, got %q: %w", s, err)
	}
	return t.Hour(), t.Minute(), nil
}

func outcome(r *Result) string {
	if r == nil {
		return "error"
	}
	if len(r.Errors) > 0 {
		return "partial"
	}
	return "ok"
}

func recordDuration(ctx context.Context, start time.Time, outcome string) {
	m, err := obs.MetricsInstance()
	if err != nil || m == nil {
		return
	}
	m.BlogMarketingSyncDuration.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(attribute.String("outcome", outcome)))
}
