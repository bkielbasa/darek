//go:build integration

package freshrssimport_test

import (
	"context"
	"sync"
	"testing"

	"darek/db"
	"darek/freshrssimport"
	"darek/internal/testutil/pg"
	"darek/links"
	"darek/tools/freshrss"

	"github.com/stretchr/testify/require"
)

// fakeFreshRSS is a stand-in for *freshrss.Client used in unit tests.
type fakeFreshRSS struct {
	articles []freshrss.Article
	mu       sync.Mutex
	marked   []string
}

func (f *fakeFreshRSS) List(ctx context.Context, opts freshrss.ListOpts) ([]freshrss.Article, error) {
	return f.articles, nil
}

func (f *fakeFreshRSS) Mark(ctx context.Context, id string, act freshrss.Action) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, id)
	return nil
}

func TestSync_ImportsAndMarksRead(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	fr := &fakeFreshRSS{
		articles: []freshrss.Article{
			{ID: "a1", URL: "https://example.com/a?utm_source=tw", Title: "Article one", Feed: "HN"},
			{ID: "a2", URL: "https://youtube.com/watch?v=x", Title: "Vid", Feed: "Channel"},
			{ID: "a3", URL: "https://example.com/a?fbclid=xyz", Title: "Dup", Feed: "Reddit"}, // canonical-dups a1
		},
	}

	res, err := freshrssimport.Sync(context.Background(), fr, store)
	require.NoError(t, err)
	require.Equal(t, 3, res.Imported, "all three should be processed")
	require.Equal(t, 3, res.MarkedRead, "all three should be marked read")
	require.Empty(t, res.Errors)

	// Two unique rows after canonicalization.
	got, err := store.Search(context.Background(), links.SearchOpts{Source: "freshrss"})
	require.NoError(t, err)
	require.Len(t, got, 2)
}
