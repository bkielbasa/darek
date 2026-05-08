//go:build integration

package blogmarketing_test

import (
	"context"
	"testing"
	"time"

	"darek/blogmarketing"
	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func TestStore_RoundTrip(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	store := blogmarketing.NewStore(db.Wrap(raw))

	ctx := context.Background()
	count, err := store.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count)

	scheduled, err := store.IsScheduled(ctx, "https://blog.example.com/a")
	require.NoError(t, err)
	require.False(t, scheduled)

	// Seen-only path (first-run backfill).
	require.NoError(t, store.MarkSeenOnly(ctx, "https://blog.example.com/old", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))

	count, err = store.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	scheduled, err = store.IsScheduled(ctx, "https://blog.example.com/old")
	require.NoError(t, err)
	require.True(t, scheduled, "seen-only rows still count as scheduled for dedup")

	// Full schedule path.
	require.NoError(t, store.MarkScheduled(ctx, "https://blog.example.com/new",
		time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
		[]string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9"},
	))

	scheduled, err = store.IsScheduled(ctx, "https://blog.example.com/new")
	require.NoError(t, err)
	require.True(t, scheduled)

	// Idempotency: re-marking is fine (PRIMARY KEY conflict swallowed via ON CONFLICT DO NOTHING).
	require.NoError(t, store.MarkSeenOnly(ctx, "https://blog.example.com/old", time.Now()))
}
