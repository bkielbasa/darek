//go:build integration

package recommend

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCandidates_FallbackToText(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	// Liked links have no embedding -> centroid is nil -> text fallback.
	seed(t, ls, "https://e.com/liked1", "Kubernetes networking deep dive", "article", intp(5))
	seed(t, ls, "https://e.com/liked2", "Kubernetes operators in practice", "article", intp(5))
	target := seed(t, ls, "https://e.com/target", "kubernetes scheduling explained", "article", nil)
	seed(t, ls, "https://e.com/cook", "sourdough bread recipe", "article", nil)

	p, err := BuildProfile(ctx, s)
	require.NoError(t, err)

	cands, vector, err := Candidates(ctx, s, p, Options{CandidatePool: 10, MinLiked: 1})
	require.NoError(t, err)
	require.False(t, vector, "no embeddings -> text path")
	require.NotEmpty(t, cands)

	var found bool
	for _, c := range cands {
		if c.ID == target {
			found = true
		}
	}
	require.True(t, found, "full-text match present")
}

func TestCandidates_VectorBalanced(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	liked := seed(t, ls, "https://e.com/liked", "liked", "article", intp(5))
	require.NoError(t, s.SetEmbedding(ctx, liked, axisVec(0)))

	art := seed(t, ls, "https://e.com/art", "close article", "article", nil)
	vid := seed(t, ls, "https://e.com/vid", "close video", "video", nil)
	require.NoError(t, s.SetEmbedding(ctx, art, axisVec(0)))
	require.NoError(t, s.SetEmbedding(ctx, vid, axisVec(0)))

	p, err := BuildProfile(ctx, s)
	require.NoError(t, err)
	require.NotNil(t, p.LikedCentroid)

	cands, vector, err := Candidates(ctx, s, p, Options{CandidatePool: 10, MinLiked: 1})
	require.NoError(t, err)
	require.True(t, vector, "embedded liked example -> vector path")

	kinds := map[string]bool{}
	for _, c := range cands {
		kinds[c.Kind] = true
	}
	require.True(t, kinds["article"], "articles represented")
	require.True(t, kinds["video"], "videos represented")
}
