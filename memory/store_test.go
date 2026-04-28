//go:build integration

package memory

import (
	"context"
	"testing"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func TestStore_SaveAndRecall(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))

	s := NewStore(pool)
	ctx := context.Background()

	_, err := s.Save(ctx, "I'm tracking a Berlin trip in May", []string{"travel"}, "user")
	require.NoError(t, err)
	_, err = s.Save(ctx, "Birthday dinner reservation 7pm", []string{"family"}, "user")
	require.NoError(t, err)

	got, err := s.Recall(ctx, "Berlin", 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Contains(t, got[0].Body, "Berlin")
}

func TestStore_RecallEmpty_ReturnsRecent(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))

	s := NewStore(pool)
	ctx := context.Background()
	_, _ = s.Save(ctx, "first", nil, "user")
	_, _ = s.Save(ctx, "second", nil, "user")

	got, err := s.Recall(ctx, "", 5)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "second", got[0].Body)
}
