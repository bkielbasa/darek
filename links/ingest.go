package links

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
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
		// Best-effort outcome=error counter.
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

// normalizeSource clamps unknown source values to "other" to bound cardinality.
func normalizeSource(s string) string {
	switch s {
	case "freshrss", "user", "email", "todoist":
		return s
	default:
		return "other"
	}
}
