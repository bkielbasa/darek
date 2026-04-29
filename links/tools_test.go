//go:build integration

package links

import (
	"context"
	"encoding/json"
	"testing"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func TestSaveTool(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	out, err := SaveTool{Store: s}.Execute(context.Background(),
		json.RawMessage(`{"url":"https://x.com/a","rating":5,"tags":["go"],"notes":"good"}`))
	require.NoError(t, err)
	require.Contains(t, out, "rating=5")
}

func TestSearchTool(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()
	five := 5
	_, _ = s.Save(ctx, SaveInput{URL: "https://x.com/a", Title: "Concurrency in Go", Rating: &five, Tags: []string{"go"}})

	out, err := SearchTool{Store: s}.Execute(ctx, json.RawMessage(`{"query":"concurrency"}`))
	require.NoError(t, err)
	require.Contains(t, out, "Concurrency in Go")
	require.Contains(t, out, "5/5")
}

func TestSimilarTool(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()
	five := 5
	_, _ = s.Save(ctx, SaveInput{URL: "u1", Title: "Go concurrency", Rating: &five, Notes: "great patterns"})

	out, err := SimilarTool{Store: s}.Execute(ctx,
		json.RawMessage(`{"text":"go concurrency","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, out, "Go concurrency")
}
