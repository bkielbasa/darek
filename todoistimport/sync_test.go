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

func TestSync_OnIngested_NewLinks(t *testing.T) {
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
		kind   string
		url    string
	}
	var (
		mu    sync.Mutex
		calls []call
	)
	onIngested := func(ctx context.Context, id uuid.UUID, kind, url, title, body string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{id, kind, url})
		return nil
	}

	res, err := todoistimport.Sync(context.Background(), fk, store, onIngested)
	require.NoError(t, err)
	require.Equal(t, 2, res.Imported)
	require.Len(t, calls, 2, "callback fires for every new link")
	byKind := map[string]string{}
	for _, c := range calls {
		require.NotEqual(t, uuid.Nil, c.linkID)
		byKind[c.kind] = c.url
	}
	require.Equal(t, "https://www.youtube.com/watch?v=abcDEF12345", byKind["video"])
	require.Equal(t, "https://example.com/article", byKind["article"])
}

func TestSync_OnIngested_NotForExistingLink(t *testing.T) {
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
	onIngested := func(ctx context.Context, id uuid.UUID, kind, url, title, body string) error {
		called++
		return nil
	}
	_, err = todoistimport.Sync(context.Background(), fk, store, onIngested)
	require.NoError(t, err)
	require.Equal(t, 0, called)
}

func TestSync_OnIngested_NilCallbackOK(t *testing.T) {
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
