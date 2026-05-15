//go:build integration

package blogmarketing_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"darek/blogmarketing"
	"darek/db"
	"darek/internal/testutil/pg"
	"darek/tools/blogfeed"
	"darek/tools/todoist"

	"github.com/stretchr/testify/require"
)

type fakeFeed struct {
	entries []blogfeed.Entry
	err     error
}

func (f *fakeFeed) List(ctx context.Context) ([]blogfeed.Entry, error) {
	return f.entries, f.err
}

type fakeDrafter struct {
	resp blogmarketing.Drafts
	err  error
}

func (f *fakeDrafter) Draft(ctx context.Context, e blogfeed.Entry, _ map[blogmarketing.Platform]string) (blogmarketing.Drafts, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	d := blogmarketing.Drafts{}
	for _, p := range blogmarketing.AllPlatforms {
		d[p] = map[blogmarketing.Cadence]string{}
		for _, c := range blogmarketing.AllCadences {
			d[p][c] = string(p) + "-" + string(c) + ":" + e.Title
		}
	}
	return d, nil
}

type fakeTodoist struct {
	mu          sync.Mutex
	projectID   string
	created     []todoist.CreateRequest
	deleted     []string
	failOnIndex int // -1 means never; 0-based index into create-call sequence
	idCounter   int
	// Map of id → task; if missing from the map, GetTask returns ErrNotFound.
	tasksByID map[string]*todoist.Task
}

func (t *fakeTodoist) ResolveProjectID(ctx context.Context, name string) (string, error) {
	if t.projectID == "" {
		return "", errors.New("project not found: " + name)
	}
	return t.projectID, nil
}

func (t *fakeTodoist) CreateTask(ctx context.Context, req todoist.CreateRequest) (*todoist.Task, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failOnIndex >= 0 && len(t.created) == t.failOnIndex {
		return nil, errors.New("simulated create failure")
	}
	t.created = append(t.created, req)
	t.idCounter++
	id := "id" + time.Now().Format("150405.000000") + "-" + string(rune('A'+t.idCounter))
	return &todoist.Task{ID: id, Content: req.Content}, nil
}

func (t *fakeTodoist) DeleteTask(ctx context.Context, id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deleted = append(t.deleted, id)
	return nil
}

func (t *fakeTodoist) GetTask(ctx context.Context, id string) (*todoist.Task, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if got, ok := t.tasksByID[id]; ok {
		return got, nil
	}
	return nil, todoist.ErrNotFound
}

func warsaw(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	return loc
}

func setup(t *testing.T) *blogmarketing.Store {
	t.Helper()
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	return blogmarketing.NewStore(db.Wrap(raw))
}

func seedEntry(url string) blogfeed.Entry {
	return blogfeed.Entry{
		CanonicalURL: url,
		URL:          url,
		Title:        "seed",
		PublishedAt:  time.Now(),
	}
}

func cfg(t *testing.T) blogmarketing.Config {
	return blogmarketing.Config{
		BlogID:      "tech-blog",
		ProjectName: "Marketing",
		PostTime:    "09:00",
		Timezone:    warsaw(t),
	}
}

func TestSync_FirstRun_MarksSeenOnly_NoTasks(t *testing.T) {
	store := setup(t)
	feed := &fakeFeed{entries: []blogfeed.Entry{
		{URL: "https://example.com/a", CanonicalURL: "https://example.com/a", Title: "A", PublishedAt: time.Now().Add(-time.Hour)},
		{URL: "https://example.com/b", CanonicalURL: "https://example.com/b", Title: "B", PublishedAt: time.Now().Add(-2 * time.Hour)},
	}}
	td := &fakeTodoist{projectID: "P", failOnIndex: -1}

	res, err := blogmarketing.Sync(context.Background(), feed, store, &fakeDrafter{}, td, cfg(t))
	require.NoError(t, err)
	require.Equal(t, 2, res.BackfillSeen)
	require.Equal(t, 0, res.Scheduled)
	require.Empty(t, td.created, "no tasks should be created on first run")

	// Second sync with the same feed: still nothing scheduled (already seen).
	res2, err := blogmarketing.Sync(context.Background(), feed, store, &fakeDrafter{}, td, cfg(t))
	require.NoError(t, err)
	require.Equal(t, 0, res2.Scheduled)
	require.Equal(t, 0, res2.BackfillSeen)
	require.Equal(t, 2, res2.Skipped)
}

func TestSync_NewPost_Schedules9Tasks(t *testing.T) {
	store := setup(t)
	// Pre-seed table so we are NOT in first-run mode.
	require.NoError(t, store.MarkSeenOnly(context.Background(), seedEntry("https://example.com/seed"), "tech-blog"))

	pubAt := time.Now().Add(-30 * time.Minute)
	feed := &fakeFeed{entries: []blogfeed.Entry{
		{URL: "https://example.com/new", CanonicalURL: "https://example.com/new", Title: "New post", PublishedAt: pubAt},
	}}
	td := &fakeTodoist{projectID: "PROJ123", failOnIndex: -1}

	res, err := blogmarketing.Sync(context.Background(), feed, store, &fakeDrafter{}, td, cfg(t))
	require.NoError(t, err)
	require.Equal(t, 1, res.Scheduled)
	require.Equal(t, 0, res.BackfillSeen)
	require.Empty(t, res.Errors)
	require.Len(t, td.created, 9, "exactly 9 Todoist tasks created")

	// Sanity-check labels and project_id propagation.
	for _, c := range td.created {
		require.Equal(t, "PROJ123", c.ProjectID)
		require.Len(t, c.Labels, 2)
		require.NotEmpty(t, c.DueDatetime)
		require.Equal(t, "https://example.com/new", c.Description)
	}

	// Confirm state row exists and re-poll is a no-op.
	scheduled, err := store.IsScheduled(context.Background(), "https://example.com/new")
	require.NoError(t, err)
	require.True(t, scheduled)
}

func TestSync_TodoistMidFailure_RollsBack(t *testing.T) {
	store := setup(t)
	require.NoError(t, store.MarkSeenOnly(context.Background(), seedEntry("https://example.com/seed"), "tech-blog"))

	feed := &fakeFeed{entries: []blogfeed.Entry{
		{URL: "https://example.com/x", CanonicalURL: "https://example.com/x", Title: "X", PublishedAt: time.Now()},
	}}
	td := &fakeTodoist{projectID: "P", failOnIndex: 5} // fail on the 6th create

	res, err := blogmarketing.Sync(context.Background(), feed, store, &fakeDrafter{}, td, cfg(t))
	require.NoError(t, err)
	require.Equal(t, 0, res.Scheduled)
	require.NotEmpty(t, res.Errors)
	require.Len(t, td.created, 5, "only 5 created before failure")
	require.Len(t, td.deleted, 5, "all 5 rolled back")

	// State row must NOT have been written → next poll retries.
	scheduled, err := store.IsScheduled(context.Background(), "https://example.com/x")
	require.NoError(t, err)
	require.False(t, scheduled)
}

func TestSync_DrafterFailure_NoTasks_NoState(t *testing.T) {
	store := setup(t)
	require.NoError(t, store.MarkSeenOnly(context.Background(), seedEntry("https://example.com/seed"), "tech-blog"))

	feed := &fakeFeed{entries: []blogfeed.Entry{
		{URL: "https://example.com/x", CanonicalURL: "https://example.com/x", Title: "X", PublishedAt: time.Now()},
	}}
	td := &fakeTodoist{projectID: "P", failOnIndex: -1}

	res, err := blogmarketing.Sync(context.Background(), feed, store, &fakeDrafter{err: errors.New("llm down")}, td, cfg(t))
	require.NoError(t, err)
	require.Equal(t, 0, res.Scheduled)
	require.NotEmpty(t, res.Errors)
	require.Empty(t, td.created)

	scheduled, err := store.IsScheduled(context.Background(), "https://example.com/x")
	require.NoError(t, err)
	require.False(t, scheduled)
}

func TestSync_FeedFailure_HardError(t *testing.T) {
	store := setup(t)
	feed := &fakeFeed{err: errors.New("dns fail")}
	td := &fakeTodoist{projectID: "P", failOnIndex: -1}
	_, err := blogmarketing.Sync(context.Background(), feed, store, &fakeDrafter{}, td, cfg(t))
	require.Error(t, err)
}

func TestSync_FirstRunIsPerBlog(t *testing.T) {
	store := setup(t)

	// Seed an unrelated blog so the table is non-empty globally.
	require.NoError(t, store.MarkSeenOnly(context.Background(),
		seedEntry("https://other.example.com/old"), "side-blog"))

	// tech-blog still has zero rows → must be treated as first-run for it,
	// even though the table itself is non-empty.
	feed := &fakeFeed{entries: []blogfeed.Entry{
		{URL: "https://example.com/a", CanonicalURL: "https://example.com/a", Title: "A", PublishedAt: time.Now()},
	}}
	td := &fakeTodoist{projectID: "P", failOnIndex: -1}

	res, err := blogmarketing.Sync(context.Background(), feed, store, &fakeDrafter{}, td, cfg(t))
	require.NoError(t, err)
	require.Equal(t, 1, res.BackfillSeen, "tech-blog must backfill its first poll even though side-blog has rows")
	require.Empty(t, td.created, "first run must not create Todoist tasks")
}

func TestSyncAll_OneBlogFailureDoesNotStopOthers(t *testing.T) {
	store := setup(t)
	// Pre-seed both blogs so neither hits first-run backfill.
	require.NoError(t, store.MarkSeenOnly(context.Background(),
		seedEntry("https://example.com/seed-a"), "blog-a"))
	require.NoError(t, store.MarkSeenOnly(context.Background(),
		seedEntry("https://example.com/seed-b"), "blog-b"))

	td := &fakeTodoist{projectID: "P", failOnIndex: -1}

	feedA := &fakeFeed{err: errors.New("dns fail")}
	feedB := &fakeFeed{entries: []blogfeed.Entry{
		{URL: "https://example.com/post-b", CanonicalURL: "https://example.com/post-b",
			Title: "Post B", PublishedAt: time.Now()},
	}}

	runs := []blogmarketing.FeedRun{
		{Feed: feedA, Config: blogmarketing.Config{BlogID: "blog-a", ProjectName: "Marketing", PostTime: "09:00", Timezone: warsaw(t)}},
		{Feed: feedB, Config: blogmarketing.Config{BlogID: "blog-b", ProjectName: "Marketing", PostTime: "09:00", Timezone: warsaw(t)}},
	}
	agg := blogmarketing.SyncAll(context.Background(), store, &fakeDrafter{}, td, runs)

	require.Equal(t, 1, agg.Scheduled, "blog-b must still schedule despite blog-a failing")
	require.NotEmpty(t, agg.Errors, "blog-a's feed error must be recorded")
	// Ensure blog-a's error is tagged with its blog id.
	found := false
	for _, err := range agg.Errors {
		if err != nil && (err.Error() == "blog blog-a: feed list: dns fail" || (len(err.Error()) > 0 && err.Error()[:13] == "blog blog-a: ")) {
			found = true
			break
		}
	}
	require.True(t, found, "blog-a error must be tagged with its blog id")
}

func TestSync_LaunchDateIsMaxOfPubAndNow(t *testing.T) {
	store := setup(t)
	require.NoError(t, store.MarkSeenOnly(context.Background(), seedEntry("https://example.com/seed"), "tech-blog"))

	loc := warsaw(t)
	old := time.Now().In(loc).Add(-72 * time.Hour) // 3 days ago
	feed := &fakeFeed{entries: []blogfeed.Entry{
		{URL: "https://example.com/late", CanonicalURL: "https://example.com/late", Title: "Late", PublishedAt: old},
	}}
	td := &fakeTodoist{projectID: "P", failOnIndex: -1}

	_, err := blogmarketing.Sync(context.Background(), feed, store, &fakeDrafter{}, td, cfg(t))
	require.NoError(t, err)
	require.Len(t, td.created, 9)

	// Find the launch task's DueDatetime — it should be today's 09:00 in Warsaw,
	// not 3 days ago.
	var launchDT string
	for _, c := range td.created {
		if c.Labels[1] == string(blogmarketing.CadenceLaunch) {
			launchDT = c.DueDatetime
			break
		}
	}
	parsed, err := time.Parse(time.RFC3339, launchDT)
	require.NoError(t, err)
	today := time.Now().In(loc)
	require.Equal(t, today.Year(), parsed.In(loc).Year())
	require.Equal(t, today.YearDay(), parsed.In(loc).YearDay())
	require.Equal(t, 9, parsed.In(loc).Hour())
}
