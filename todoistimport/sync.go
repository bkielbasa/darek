package todoistimport

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"darek/links"
	"darek/obs"
	"darek/tools/todoist"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/errgroup"
)

var tracer = otel.Tracer("darek/todoistimport")

// concurrency is the maximum number of tasks processed in parallel by Sync.
// Bounded so we don't hammer Todoist's rate limit or exhaust the pgx pool.
const concurrency = 5

// Lister is the subset of *todoist.Client used by Sync. Defined as an
// interface so tests can supply a fake.
type Lister interface {
	ListTasks(ctx context.Context, f todoist.ListFilter) ([]todoist.Task, error)
	CompleteTask(ctx context.Context, id string) error
}

// Result summarizes a sync run.
type Result struct {
	Imported  int
	Completed int
	Skipped   int // tasks without a URL
	Errors    []error
}

// OnVideoIngestedFunc is called once per newly-ingested video link
// (kind=="video", isNew=true). Errors are appended to Result.Errors but do
// not abort sync.
type OnVideoIngestedFunc func(ctx context.Context, linkID uuid.UUID, url, title string) error

var urlRE = regexp.MustCompile(`https?://[^\s<>"']+`)

// extractURL returns the first http(s) URL found in content, or in description
// if content has none. Returns "" if neither has one.
func extractURL(content, description string) string {
	if m := urlRE.FindString(content); m != "" {
		return m
	}
	return urlRE.FindString(description)
}

// normalizeLabels lowercases, trims, drops blanks, and dedupes labels for use
// as tags.
func normalizeLabels(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, l := range in {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	return out
}

// Sync pulls #Inbox tasks, ingests URL-bearing ones via links.IngestOne,
// merges Todoist labels into the link's tags, and completes the task.
// Tasks without URLs are left alone (not completed).
func Sync(ctx context.Context, c Lister, store *links.Store, onVideoIngested OnVideoIngestedFunc) (*Result, error) {
	ctx, span := tracer.Start(ctx, "todoistimport.sync")
	defer span.End()

	start := time.Now()
	res := &Result{}

	tasks, err := c.ListTasks(ctx, todoist.ListFilter{Filter: "#Inbox"})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		recordDuration(ctx, start, "error")
		return nil, fmt.Errorf("list inbox: %w", err)
	}

	// Process tasks in parallel with a bounded worker pool. Each goroutine
	// writes to its own index in `outcomes` so no mutex is needed; the loop
	// after Wait aggregates into res.
	outcomes := make([]taskOutcome, len(tasks))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)
	for i, t := range tasks {
		g.Go(func() error {
			outcomes[i] = processTask(gctx, c, store, t, onVideoIngested)
			return nil // never abort siblings — per-item errors are collected
		})
	}
	_ = g.Wait()

	for _, o := range outcomes {
		switch {
		case o.Skipped:
			res.Skipped++
		case o.Imported:
			res.Imported++
			if o.Completed {
				res.Completed++
			}
		}
		if o.Err != nil {
			res.Errors = append(res.Errors, o.Err)
		}
	}

	outcome := "ok"
	if len(res.Errors) > 0 {
		outcome = "partial"
	}
	span.SetAttributes(
		attribute.Int("imported", res.Imported),
		attribute.Int("completed", res.Completed),
		attribute.Int("skipped", res.Skipped),
		attribute.Int("errors", len(res.Errors)),
	)
	recordDuration(ctx, start, outcome)
	return res, nil
}

// taskOutcome is the per-task result populated by processTask. Goroutines
// write into a fixed-index slice so no mutex is needed.
type taskOutcome struct {
	Imported  bool
	Completed bool
	Skipped   bool
	Err       error
}

// processTask runs the ingest/label/complete pipeline for a single task and
// returns its outcome. Pure function over (ctx, c, store, t); safe to call
// concurrently for distinct tasks.
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

func recordDuration(ctx context.Context, start time.Time, outcome string) {
	m, err := obs.MetricsInstance()
	if err != nil || m == nil {
		return
	}
	m.TodoistSyncDuration.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(attribute.String("outcome", outcome)))
}
