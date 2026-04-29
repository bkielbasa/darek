package freshrssimport

import (
	"context"
	"fmt"
	"time"

	"darek/links"
	"darek/obs"
	"darek/tools/freshrss"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Lister is the subset of *freshrss.Client used by Sync. Defined as an
// interface so tests can supply a fake.
type Lister interface {
	List(ctx context.Context, opts freshrss.ListOpts) ([]freshrss.Article, error)
	Mark(ctx context.Context, id string, act freshrss.Action) error
}

// Result summarizes a sync run.
type Result struct {
	Imported   int
	MarkedRead int
	Skipped    int
	Errors     []error
}

// Sync pulls all unread FreshRSS articles, ingests them via links.IngestOne
// (deduping on canonical URL), and marks each successfully-ingested article
// as read. Per-article errors are collected and returned; they don't abort
// the run.
func Sync(ctx context.Context, fr Lister, store *links.Store) (*Result, error) {
	start := time.Now()
	res := &Result{}

	arts, err := fr.List(ctx, freshrss.ListOpts{Filter: freshrss.FilterUnread, Limit: 1000})
	if err != nil {
		recordDuration(ctx, start, "error")
		return nil, fmt.Errorf("list unread: %w", err)
	}

	for _, a := range arts {
		if a.URL == "" {
			res.Skipped++
			continue
		}
		_, _, err := links.IngestOne(ctx, store, links.Candidate{
			URL:    a.URL,
			Title:  a.Title,
			Source: "freshrss",
			Feed:   a.Feed,
		})
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("ingest %s: %w", a.ID, err))
			continue
		}
		res.Imported++
		if err := fr.Mark(ctx, a.ID, freshrss.ActionMarkRead); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("mark %s read: %w", a.ID, err))
			continue
		}
		res.MarkedRead++
	}

	outcome := "ok"
	if len(res.Errors) > 0 {
		outcome = "partial"
	}
	recordDuration(ctx, start, outcome)
	return res, nil
}

func recordDuration(ctx context.Context, start time.Time, outcome string) {
	m, err := obs.MetricsInstance()
	if err != nil || m == nil {
		return
	}
	m.FreshRSSSyncDuration.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(attribute.String("outcome", outcome)))
}
