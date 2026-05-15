//go:build integration

package blogmarketing_test

import (
	"context"
	"testing"
	"time"

	"darek/blogmarketing"
	"darek/db"
	"darek/internal/testutil/pg"
	"darek/tools/blogfeed"

	"github.com/stretchr/testify/require"
)

func TestStore_RoundTrip(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := blogmarketing.NewStore(db.Wrap(raw))

	ctx := context.Background()
	count, err := store.Count(ctx, "tech-blog")
	require.NoError(t, err)
	require.Equal(t, 0, count)

	scheduled, err := store.IsScheduled(ctx, "https://blog.example.com/a")
	require.NoError(t, err)
	require.False(t, scheduled)

	// Seen-only path (per-blog first-run backfill).
	require.NoError(t, store.MarkSeenOnly(ctx, blogfeed.Entry{
		CanonicalURL: "https://blog.example.com/old",
		URL:          "https://blog.example.com/old",
		Title:        "Old Post",
		Summary:      "Old summary",
		PublishedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, "tech-blog"))

	count, err = store.Count(ctx, "tech-blog")
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Count is per-blog: a different blog still reads as first-run.
	count, err = store.Count(ctx, "side-blog")
	require.NoError(t, err)
	require.Equal(t, 0, count, "Count must scope to blog_id so adding a new blog doesn't drag in old rows")

	scheduled, err = store.IsScheduled(ctx, "https://blog.example.com/old")
	require.NoError(t, err)
	require.True(t, scheduled, "seen-only rows still count as scheduled for dedup")

	// Full schedule path: build a 9-cell ref slice and persist.
	refs := make([]blogmarketing.TaskRef, 0, 9)
	for _, p := range blogmarketing.AllPlatforms {
		for _, c := range blogmarketing.AllCadences {
			refs = append(refs, blogmarketing.TaskRef{
				Platform:  p,
				Cadence:   c,
				TodoistID: "id-" + string(p) + "-" + string(c),
			})
		}
	}
	require.NoError(t, store.SaveTasks(ctx, blogfeed.Entry{
		CanonicalURL: "https://blog.example.com/new",
		URL:          "https://blog.example.com/new?utm=rss",
		Title:        "New post",
		Summary:      "Hello world",
		PublishedAt:  time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
	}, "tech-blog", refs))

	scheduled, err = store.IsScheduled(ctx, "https://blog.example.com/new")
	require.NoError(t, err)
	require.True(t, scheduled)

	// Round-trip: GetTasks returns the same set.
	got, err := store.GetTasks(ctx, "https://blog.example.com/new")
	require.NoError(t, err)
	require.Len(t, got, 9)

	// Reverse-lookup: GetTaskState resolves any one of the ids back to its cell.
	state, err := store.GetTaskState(ctx, "id-x-launch")
	require.NoError(t, err)
	require.Equal(t, "https://blog.example.com/new", state.CanonicalURL)
	require.Equal(t, "tech-blog", state.BlogID)
	require.Equal(t, blogmarketing.PlatformX, state.Platform)
	require.Equal(t, blogmarketing.CadenceLaunch, state.Cadence)
	require.Nil(t, state.PostedAt, "freshly-saved task has no posted_at yet")

	// MarkPosted updates posted_at + posted_url; GetTaskState picks up both.
	require.NoError(t, store.MarkPosted(ctx, "id-x-launch", "https://fosstodon.org/@bk/abc"))
	state, err = store.GetTaskState(ctx, "id-x-launch")
	require.NoError(t, err)
	require.NotNil(t, state.PostedAt)
	require.Equal(t, "https://fosstodon.org/@bk/abc", state.PostedURL)

	// MarkPosted on an unknown id reports the miss explicitly.
	require.Error(t, store.MarkPosted(ctx, "no-such-id", "url"))

	// GetEntry reads back the persisted meta verbatim, so a future regenerate
	// has everything it needs without re-fetching the feed.
	entry, err := store.GetEntry(ctx, "https://blog.example.com/new")
	require.NoError(t, err)
	require.Equal(t, "New post", entry.Title)
	require.Equal(t, "Hello world", entry.Summary)
	require.Equal(t, "https://blog.example.com/new?utm=rss", entry.URL)

	// Idempotency: re-marking is fine (PRIMARY KEY conflict swallowed via ON CONFLICT DO NOTHING).
	require.NoError(t, store.MarkSeenOnly(ctx, blogfeed.Entry{
		CanonicalURL: "https://blog.example.com/old",
		Title:        "x",
		PublishedAt:  time.Now(),
	}, "tech-blog"))
}
