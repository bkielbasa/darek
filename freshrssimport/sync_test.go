//go:build integration

package freshrssimport_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"darek/db"
	"darek/freshrssimport"
	"darek/internal/testutil/pg"
	"darek/links"
	"darek/tools/freshrss"

	"github.com/google/uuid"
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

	res, err := freshrssimport.Sync(context.Background(), fr, store, nil)
	require.NoError(t, err)
	require.Equal(t, 3, res.Imported, "all three should be processed")
	require.Equal(t, 3, res.MarkedRead, "all three should be marked read")
	require.Empty(t, res.Errors)

	// Two unique rows after canonicalization.
	got, err := store.Search(context.Background(), links.SearchOpts{Source: "freshrss"})
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestSync_OnVideoIngested_NewVideo(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	fr := &fakeFreshRSS{
		articles: []freshrss.Article{
			{ID: "1", URL: "https://www.youtube.com/watch?v=abcDEF12345", Title: "vid", Feed: "f"},
			{ID: "2", URL: "https://example.com/an-article", Title: "art", Feed: "f"},
		},
	}

	type call struct {
		linkID uuid.UUID
		url    string
		title  string
	}
	var (
		mu    sync.Mutex
		calls []call
	)
	onVideo := func(ctx context.Context, id uuid.UUID, url, title string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{id, url, title})
		return nil
	}

	res, err := freshrssimport.Sync(context.Background(), fr, store, onVideo)
	require.NoError(t, err)
	require.Equal(t, 2, res.Imported)
	require.Len(t, calls, 1, "callback fires exactly once for the video")
	require.Equal(t, "https://www.youtube.com/watch?v=abcDEF12345", calls[0].url)
	require.Equal(t, "vid", calls[0].title)
	require.NotEqual(t, uuid.Nil, calls[0].linkID)
}

func TestSync_OnVideoIngested_NotForExistingVideo(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	url := "https://www.youtube.com/watch?v=abcDEF12345"
	_, _, _, err := links.IngestOne(context.Background(), store, links.Candidate{
		URL: url, Title: "vid", Source: "user",
	})
	require.NoError(t, err)

	fr := &fakeFreshRSS{
		articles: []freshrss.Article{{ID: "1", URL: url, Title: "vid", Feed: "f"}},
	}
	called := 0
	onVideo := func(ctx context.Context, id uuid.UUID, url, title string) error {
		called++
		return nil
	}
	_, err = freshrssimport.Sync(context.Background(), fr, store, onVideo)
	require.NoError(t, err)
	require.Equal(t, 0, called, "callback must not fire for existing rows")
}

func TestSync_OnVideoIngested_CallbackErrorContinues(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	fr := &fakeFreshRSS{
		articles: []freshrss.Article{
			{ID: "1", URL: "https://www.youtube.com/watch?v=abcDEF12345", Title: "v1", Feed: "f"},
			{ID: "2", URL: "https://www.youtube.com/watch?v=zzzZZZ98765", Title: "v2", Feed: "f"},
		},
	}

	onVideo := func(ctx context.Context, id uuid.UUID, url, title string) error {
		return errors.New("boom")
	}
	res, err := freshrssimport.Sync(context.Background(), fr, store, onVideo)
	require.NoError(t, err)
	require.Equal(t, 2, res.Imported, "both videos still imported")
	require.GreaterOrEqual(t, len(res.Errors), 2, "both callback errors collected")
}

func TestSync_OnVideoIngested_NilCallbackOK(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	fr := &fakeFreshRSS{
		articles: []freshrss.Article{
			{ID: "1", URL: "https://www.youtube.com/watch?v=abcDEF12345", Title: "v1", Feed: "f"},
		},
	}

	res, err := freshrssimport.Sync(context.Background(), fr, store, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Imported)
}
