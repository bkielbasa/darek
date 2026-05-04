//go:build integration

package todoistimport_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"darek/db"
	"darek/internal/testutil/pg"
	"darek/links"
	"darek/todoistimport"
	"darek/tools/todoist"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// fakeTodoist stands in for *todoist.Client.
type fakeTodoist struct {
	tasks []todoist.Task

	mu        sync.Mutex
	completed []string
	failNext  bool
}

func (f *fakeTodoist) ListTasks(ctx context.Context, _ todoist.ListFilter) ([]todoist.Task, error) {
	return f.tasks, nil
}

func (f *fakeTodoist) CompleteTask(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return errors.New("complete failed")
	}
	f.completed = append(f.completed, id)
	return nil
}

func TestSync_MixedInbox(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	fk := &fakeTodoist{
		tasks: []todoist.Task{
			{ID: "t1", Content: "Read https://example.com/a sometime"},
			{ID: "t2", Content: "Buy milk"},
			{ID: "t3", Content: "good post", Description: "Body has https://example.com/b deep inside"},
		},
	}
	res, err := todoistimport.Sync(context.Background(), fk, store, nil)
	require.NoError(t, err)
	require.Equal(t, 2, res.Imported)
	require.Equal(t, 2, res.Completed)
	require.Equal(t, 1, res.Skipped)
	require.Empty(t, res.Errors)

	require.ElementsMatch(t, []string{"t1", "t3"}, fk.completed)

	got, err := store.Search(context.Background(), links.SearchOpts{Source: "todoist"})
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestSync_LabelsMergeIntoTags(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	fk := &fakeTodoist{
		tasks: []todoist.Task{
			{ID: "t1", Content: "https://example.com/x", Labels: []string{"Go", "  CONCURRENCY  "}},
		},
	}
	_, err := todoistimport.Sync(context.Background(), fk, store, nil)
	require.NoError(t, err)

	got, err := store.Search(context.Background(), links.SearchOpts{Source: "todoist"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.ElementsMatch(t, []string{"go", "concurrency"}, got[0].Tags)
}

func TestSync_NoURLTaskIsSkipped(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	fk := &fakeTodoist{
		tasks: []todoist.Task{
			// "noscheme://broken" doesn't match the http(s) regex.
			{ID: "t1", Content: "noscheme://broken"},
		},
	}
	res, err := todoistimport.Sync(context.Background(), fk, store, nil)
	require.NoError(t, err)
	require.Equal(t, 0, res.Imported)
	require.Equal(t, 1, res.Skipped)
	require.Empty(t, fk.completed)
}

func TestSync_OnVideoIngested_NewVideo(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	fk := &fakeTodoist{
		tasks: []todoist.Task{
			{ID: "1", Content: "watch this https://www.youtube.com/watch?v=abcDEF12345"},
			{ID: "2", Content: "read https://example.com/article"},
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

	res, err := todoistimport.Sync(context.Background(), fk, store, onVideo)
	require.NoError(t, err)
	require.Equal(t, 2, res.Imported)
	require.Len(t, calls, 1, "callback fires exactly once for the video")
	require.Equal(t, "https://www.youtube.com/watch?v=abcDEF12345", calls[0].url)
	require.NotEqual(t, uuid.Nil, calls[0].linkID)
}

func TestSync_OnVideoIngested_NotForExistingVideo(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	url := "https://www.youtube.com/watch?v=abcDEF12345"
	_, _, _, err := links.IngestOne(context.Background(), store, links.Candidate{
		URL: url, Title: "v", Source: "user",
	})
	require.NoError(t, err)

	fk := &fakeTodoist{tasks: []todoist.Task{{ID: "1", Content: url}}}
	called := 0
	_, err = todoistimport.Sync(context.Background(), fk, store, func(ctx context.Context, id uuid.UUID, url, title string) error {
		called++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 0, called)
}

func TestSync_OnVideoIngested_NilCallbackOK(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := links.NewStore(db.Wrap(raw))

	fk := &fakeTodoist{
		tasks: []todoist.Task{
			{ID: "1", Content: "https://www.youtube.com/watch?v=abcDEF12345"},
		},
	}

	res, err := todoistimport.Sync(context.Background(), fk, store, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Imported)
}
