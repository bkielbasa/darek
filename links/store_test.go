//go:build integration

package links

import (
	"context"
	"testing"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func TestStore_SaveAndSearch(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	five := 5
	_, err := s.Save(ctx, SaveInput{
		URL:    "https://example.com/a",
		Title:  "Go Concurrency Patterns",
		Rating: &five,
		Tags:   []string{"Go", "concurrency"},
		Notes:  "core reading",
	})
	require.NoError(t, err)

	got, err := s.Search(ctx, SearchOpts{Query: "concurrency"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "Go Concurrency Patterns", got[0].Title)
	require.Equal(t, []string{"go", "concurrency"}, got[0].Tags) // lowercased
}

func TestStore_Save_UpsertMergesTags(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	id1, err := s.Save(ctx, SaveInput{URL: "u", Tags: []string{"a"}})
	require.NoError(t, err)
	id2, err := s.Save(ctx, SaveInput{URL: "u", Tags: []string{"b"}})
	require.NoError(t, err)
	require.Equal(t, id1, id2)

	got, err := s.Search(ctx, SearchOpts{})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a", "b"}, got[0].Tags)
}

func TestStore_Save_ReplaceTags(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	_, _ = s.Save(ctx, SaveInput{URL: "u", Tags: []string{"a", "b"}})
	_, _ = s.Save(ctx, SaveInput{URL: "u", Tags: []string{"c"}, ReplaceTags: true})
	got, _ := s.Search(ctx, SearchOpts{})
	require.Equal(t, []string{"c"}, got[0].Tags)
}

func TestStore_Save_RatingValidation(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()
	bad := 6
	_, err := s.Save(ctx, SaveInput{URL: "u", Rating: &bad})
	require.Error(t, err)
}

func TestStore_Search_MinRating(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()
	r5 := 5
	r2 := 2
	_, _ = s.Save(ctx, SaveInput{URL: "u1", Title: "loved", Rating: &r5})
	_, _ = s.Save(ctx, SaveInput{URL: "u2", Title: "meh", Rating: &r2})
	_, _ = s.Save(ctx, SaveInput{URL: "u3", Title: "unrated"})

	got, err := s.Search(ctx, SearchOpts{MinRating: 4})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "loved", got[0].Title)
}

func TestStore_Similar_OnlyRated(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()
	r5 := 5
	_, _ = s.Save(ctx, SaveInput{URL: "u1", Title: "Go concurrency", Tags: []string{"go"}, Rating: &r5, Notes: "great"})
	_, _ = s.Save(ctx, SaveInput{URL: "u2", Title: "Go concurrency advanced", Tags: []string{"go"}, Notes: "no rating"})

	got, err := s.Similar(ctx, "go concurrency", 5)
	require.NoError(t, err)
	require.Len(t, got, 1) // unrated excluded
	require.Equal(t, "Go concurrency", got[0].Title)
}

func TestStore_Delete_ByURL(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()
	_, _ = s.Save(ctx, SaveInput{URL: "u"})
	require.NoError(t, s.Delete(ctx, [16]byte{}, "u"))
	got, _ := s.Search(ctx, SearchOpts{})
	require.Empty(t, got)
}

func TestStore_ApplyAnalysis(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	// Seed a row with one existing tag.
	id, err := s.Save(ctx, SaveInput{
		URL:    "https://example.com/x",
		Title:  "X",
		Tags:   []string{"existing"},
		Source: "user",
	})
	require.NoError(t, err)

	err = s.ApplyAnalysis(ctx, id, "ai summary", []string{"new", "existing"})
	require.NoError(t, err)

	got, err := s.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "ai summary", got.Summary)
	require.NotNil(t, got.AnalyzedAt)
	require.ElementsMatch(t, []string{"existing", "new"}, got.Tags)
}
