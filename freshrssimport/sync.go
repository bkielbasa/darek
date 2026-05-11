package freshrssimport

import (
	"context"
	"fmt"
	"time"

	"darek/exechistory"
	"darek/links"
	"darek/obs"
	"darek/tools/freshrss"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/errgroup"
)

var tracer = otel.Tracer("darek/freshrssimport")

// concurrency is the maximum number of articles processed in parallel by Sync.
// Bounded so we don't hammer FreshRSS or exhaust the pgx pool.
const concurrency = 5

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

// OnVideoIngestedFunc is called once per newly-ingested video link
// (kind=="video", isNew=true). Errors are appended to Result.Errors but do
// not abort sync. Implementations are typically a closure that wraps an
// analyze step and persists summary+tags via *links.Store.ApplyAnalysis.
type OnVideoIngestedFunc func(ctx context.Context, linkID uuid.UUID, url, title string) error

// Sync pulls all unread FreshRSS articles, ingests them via links.IngestOne
// (deduping on canonical URL), and marks each successfully-ingested article
// as read. Per-article errors are collected and returned; they don't abort
// the run.
func Sync(ctx context.Context, fr Lister, store *links.Store, onVideoIngested OnVideoIngestedFunc) (*Result, error) {
	ctx, span := tracer.Start(ctx, "freshrssimport.sync")
	exechistory.MarkExecution(span, "freshrss-sync")
	defer span.End()

	start := time.Now()
	res := &Result{}

	arts, err := fr.List(ctx, freshrss.ListOpts{Filter: freshrss.FilterUnread, Limit: 1000})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		recordDuration(ctx, start, "error")
		return nil, fmt.Errorf("list unread: %w", err)
	}

	// Process articles in parallel with a bounded worker pool. Each goroutine
	// writes to its own index in `outcomes` so no mutex is needed; the loop
	// after Wait aggregates into res.
	outcomes := make([]articleOutcome, len(arts))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)
	for i, a := range arts {
		g.Go(func() error {
			outcomes[i] = processArticle(gctx, fr, store, a, onVideoIngested)
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
			if o.MarkedRead {
				res.MarkedRead++
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
		attribute.Int("marked_read", res.MarkedRead),
		attribute.Int("skipped", res.Skipped),
		attribute.Int("errors", len(res.Errors)),
	)
	recordDuration(ctx, start, outcome)
	return res, nil
}

// articleOutcome is the per-article result populated by processArticle.
// Goroutines write into a fixed-index slice so no mutex is needed.
type articleOutcome struct {
	Imported   bool
	MarkedRead bool
	Skipped    bool
	Err        error
}

// processArticle runs the ingest/mark-read pipeline for a single article and
// returns its outcome. Pure function over (ctx, fr, store, a); safe to call
// concurrently for distinct articles.
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
			if o.Err == nil {
				o.Err = fmt.Errorf("auto-analyze %s: %w", a.ID, cbErr)
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
	m.FreshRSSSyncDuration.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(attribute.String("outcome", outcome)))
}
