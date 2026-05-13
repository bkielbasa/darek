package links

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
)

// Candidate is a URL produced by an ingestion source (RSS, email, manual UI,
// agent tools). Sources fill what they know; IngestOne handles the rest.
type Candidate struct {
	URL     string // raw URL from the source (canonicalized inside IngestOne)
	Title   string
	Source  string // "freshrss" | "email" | "user" | …
	Feed    string // RSS feed name or other origin label; optional
	Kind    string // optional override; classifier runs if empty
	Summary string // optional source-provided summary; HTML stripped before storage
}

// IngestOne canonicalizes the URL, infers kind if unset, and upserts via the
// store. Returns the resulting link id, whether it was a brand-new row, and
// the resolved kind.
//
// Wrapped in a links.ingest_one span (tracer scope "darek/links"). The two
// DB operations underneath get their own child spans (links.lookup,
// links.save) so the executions waterfall shows where DB time is spent.
//
// On metrics failure, ingestion proceeds without recording (matches the
// "instrumentation never blocks real work" contract).
func IngestOne(ctx context.Context, store *Store, c Candidate) (uuid.UUID, bool, string, error) {
	ctx, span := tracer.Start(ctx, "links.ingest_one")
	defer span.End()
	span.SetAttributes(
		attribute.String("link.source", normalizeSource(c.Source)),
		attribute.String("link.url_raw", truncURL(c.URL)),
	)

	if store == nil {
		err := errors.New("links.IngestOne: store is required")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return uuid.Nil, false, "", err
	}
	canon := Canonicalize(c.URL)
	if canon == "" {
		err := fmt.Errorf("links.IngestOne: unparseable url %q", c.URL)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return uuid.Nil, false, "", err
	}
	span.SetAttributes(attribute.String("link.url", truncURL(canon)))

	kind := c.Kind
	if kind == "" {
		kind = Classify(canon)
	}
	span.SetAttributes(attribute.String("link.kind", kind))

	// Lookup is the existence check. Wrap it in its own span so the
	// waterfall shows where DB time is spent for this article.
	isNew := false
	{
		lookupCtx, lookupSpan := tracer.Start(ctx, "links.lookup")
		var existingID uuid.UUID
		err := store.pool.QueryRow(lookupCtx, `SELECT id FROM links WHERE url = $1`, canon).Scan(&existingID)
		if errors.Is(err, ErrNoRows()) {
			isNew = true
		} else if err != nil {
			lookupSpan.RecordError(err)
			lookupSpan.SetStatus(codes.Error, err.Error())
			lookupSpan.End()
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return uuid.Nil, false, "", fmt.Errorf("links.IngestOne lookup: %w", err)
		}
		lookupSpan.End()
	}
	span.SetAttributes(attribute.Bool("link.is_new", isNew))

	saveCtx, saveSpan := tracer.Start(ctx, "links.save")
	id, err := store.Save(saveCtx, SaveInput{
		URL:     canon,
		Title:   c.Title,
		Source:  c.Source,
		Kind:    kind,
		Feed:    c.Feed,
		Summary: StripHTML(c.Summary),
	})
	if err != nil {
		saveSpan.RecordError(err)
		saveSpan.SetStatus(codes.Error, err.Error())
		saveSpan.End()
		if store.m != nil {
			store.m.LinksIngest.Add(ctx, 1, metric.WithAttributes(
				attribute.String("source", normalizeSource(c.Source)),
				attribute.String("kind", kind),
				attribute.String("outcome", "error"),
			))
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return uuid.Nil, false, kind, err
	}
	saveSpan.End()
	if store.m != nil {
		store.m.LinksIngest.Add(ctx, 1, metric.WithAttributes(
			attribute.String("source", normalizeSource(c.Source)),
			attribute.String("kind", kind),
			attribute.String("outcome", "ok"),
		))
	}
	return id, isNew, kind, nil
}

// normalizeSource clamps unknown source values to "other" to bound cardinality.
func normalizeSource(s string) string {
	switch s {
	case "freshrss", "user", "email", "todoist":
		return s
	default:
		return "other"
	}
}
