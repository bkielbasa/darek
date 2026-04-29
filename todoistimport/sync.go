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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
)

var tracer = otel.Tracer("darek/todoistimport")

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
func Sync(ctx context.Context, c Lister, store *links.Store) (*Result, error) {
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

	for _, t := range tasks {
		rawURL := extractURL(t.Content, t.Description)
		if rawURL == "" {
			res.Skipped++
			continue
		}
		title := strings.TrimSpace(strings.ReplaceAll(t.Content, rawURL, ""))
		if title == "" {
			title = rawURL
		}
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
				if _, err := store.Pool().Exec(ctx,
					`UPDATE links SET tags = ARRAY(SELECT DISTINCT unnest(tags || $2::text[])), updated_at = now() WHERE id = $1`,
					id, tags); err != nil {
					res.Errors = append(res.Errors, fmt.Errorf("merge labels %s: %w", t.ID, err))
					continue
				}
			}
		}

		if err := c.CompleteTask(ctx, t.ID); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("complete %s: %w", t.ID, err))
			continue
		}
		res.Completed++
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

func recordDuration(ctx context.Context, start time.Time, outcome string) {
	m, err := obs.MetricsInstance()
	if err != nil || m == nil {
		return
	}
	m.TodoistSyncDuration.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(attribute.String("outcome", outcome)))
}
