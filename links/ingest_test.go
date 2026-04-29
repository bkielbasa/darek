//go:build integration

package links_test

import (
	"context"
	"testing"

	"darek/db"
	"darek/internal/testutil/pg"
	"darek/links"

	"github.com/stretchr/testify/require"
)

func TestIngestOne_Insert(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))
	ctx := context.Background()

	id, isNew, err := links.IngestOne(ctx, store, links.Candidate{
		URL:    "https://www.example.com/article?utm_source=twitter",
		Title:  "Hello",
		Source: "freshrss",
		Feed:   "Hacker News",
	})
	require.NoError(t, err)
	require.True(t, isNew)
	require.NotEqual(t, "00000000-0000-0000-0000-000000000000", id.String())

	// Same URL, different referrer params → same canonical → upsert (not new).
	_, isNew2, err := links.IngestOne(ctx, store, links.Candidate{
		URL:    "https://example.com/article?fbclid=xyz",
		Source: "user",
	})
	require.NoError(t, err)
	require.False(t, isNew2, "expected upsert, got insert (canonicalization broken?)")

	// Verify stored row has the canonical URL and inferred kind.
	got, err := store.Search(ctx, links.SearchOpts{Query: "Hello"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "https://example.com/article", got[0].URL)
	require.Equal(t, "article", got[0].Kind)
	require.Equal(t, "Hacker News", got[0].Feed)
	require.Equal(t, "freshrss", got[0].Source)
}

func TestIngestOne_KindClassifierApplies(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))
	ctx := context.Background()

	_, _, err := links.IngestOne(ctx, store, links.Candidate{
		URL:    "https://youtube.com/watch?v=abc",
		Source: "freshrss",
	})
	require.NoError(t, err)
	got, err := store.Search(ctx, links.SearchOpts{Source: "freshrss"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "video", got[0].Kind)
}

func TestIngestOne_RejectsUnparseableURL(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	_, _, err := links.IngestOne(context.Background(), store, links.Candidate{
		URL:    "not a url",
		Source: "freshrss",
	})
	require.Error(t, err)
}
