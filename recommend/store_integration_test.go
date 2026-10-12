//go:build integration

package recommend

import (
	"context"
	"testing"

	"darek/db"
	"darek/internal/testutil/pg"
	"darek/links"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// newRecStore starts a fresh database and returns a recommend Store plus the
// underlying links Store for seeding.
func newRecStore(t *testing.T) (*Store, *links.Store) {
	t.Helper()
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	wrapped := db.Wrap(pool)
	return NewStore(wrapped), links.NewStore(wrapped)
}

// axisVec returns a 1536-dim unit vector pointing along axis i.
func axisVec(i int) []float32 {
	v := make([]float32, 1536)
	v[i] = 1
	return v
}

func seed(t *testing.T, ls *links.Store, url, title, kind string, rating *int) uuid.UUID {
	t.Helper()
	id, err := ls.Save(context.Background(), links.SaveInput{
		URL: url, Title: title, Kind: kind, Source: "test", Rating: rating,
	})
	require.NoError(t, err)
	return id
}

func intp(i int) *int { return &i }

func TestPendingEmbeddings(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	a := seed(t, ls, "https://e.com/a", "article a", "article", nil)
	seed(t, ls, "https://e.com/b", "article b", "article", nil)

	pending, err := s.PendingEmbeddings(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 2)

	require.NoError(t, s.SetEmbedding(ctx, a, axisVec(0)))

	pending, err = s.PendingEmbeddings(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "article b", pending[0].Text)
}

func TestRatedExamples(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	liked := seed(t, ls, "https://e.com/liked", "liked", "article", intp(5))
	disliked := seed(t, ls, "https://e.com/disliked", "disliked", "article", intp(2))
	mid := seed(t, ls, "https://e.com/mid", "mid", "article", intp(3))
	noEmb := seed(t, ls, "https://e.com/noemb", "no embedding", "article", intp(5))

	require.NoError(t, s.SetEmbedding(ctx, liked, axisVec(0)))
	require.NoError(t, s.SetEmbedding(ctx, disliked, axisVec(1)))
	require.NoError(t, s.SetEmbedding(ctx, mid, axisVec(2)))
	_ = noEmb

	good, err := s.RatedExamples(ctx, 4, 5, 10)
	require.NoError(t, err)
	require.Len(t, good, 1, "only the embedded rating>=4 example")
	require.Equal(t, liked, good[0].ID)

	bad, err := s.RatedExamples(ctx, 1, 2, 10)
	require.NoError(t, err)
	require.Len(t, bad, 1)
	require.Equal(t, disliked, bad[0].ID)
}

func TestVectorCandidates_OrderAndCooldown(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	liked := seed(t, ls, "https://e.com/liked", "liked", "article", intp(5))
	require.NoError(t, s.SetEmbedding(ctx, liked, axisVec(0)))

	a := seed(t, ls, "https://e.com/a", "close article", "article", nil)
	b := seed(t, ls, "https://e.com/b", "far article", "article", nil)
	v := seed(t, ls, "https://e.com/v", "close video", "video", nil)
	require.NoError(t, s.SetEmbedding(ctx, a, axisVec(0)))
	require.NoError(t, s.SetEmbedding(ctx, b, axisVec(1)))
	require.NoError(t, s.SetEmbedding(ctx, v, axisVec(0)))

	cands, err := s.VectorCandidates(ctx, axisVec(0), nil, 0, 0, 5)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(cands), 3)
	require.Equal(t, a, cands[0].ID, "closest article first")
	require.Equal(t, v, cands[1].ID, "articles and videos interleaved")

	// Once recommended, a candidate drops out (cooldownDays=0 => never repeat).
	require.NoError(t, s.SaveRecommendations(ctx, []Suggestion{{Candidate: cands[0]}}, "digest"))
	cands2, err := s.VectorCandidates(ctx, axisVec(0), nil, 0, 0, 5)
	require.NoError(t, err)
	for _, c := range cands2 {
		require.NotEqual(t, a, c.ID, "already-recommended link excluded")
	}
}

func TestTextCandidates(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	seed(t, ls, "https://e.com/k8s", "kubernetes networking deep dive", "article", nil)
	seed(t, ls, "https://e.com/cook", "sourdough bread recipe", "article", nil)

	cands, err := s.TextCandidates(ctx, "kubernetes", 0, 0, 5)
	require.NoError(t, err)
	require.Len(t, cands, 1)
	require.Equal(t, "https://e.com/k8s", cands[0].URL)
}
