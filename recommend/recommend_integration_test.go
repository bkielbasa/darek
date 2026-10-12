//go:build integration

package recommend

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuild_VectorPicksAndPersists(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	liked := seed(t, ls, "https://e.com/liked", "liked", "article", intp(5))
	require.NoError(t, s.SetEmbedding(ctx, liked, axisVec(0)))

	a := seed(t, ls, "https://e.com/a", "close article", "article", nil)
	b := seed(t, ls, "https://e.com/b", "far article", "article", nil)
	require.NoError(t, s.SetEmbedding(ctx, a, axisVec(0)))
	require.NoError(t, s.SetEmbedding(ctx, b, axisVec(1)))

	fc := &fakeSelectorChat{content: `{"recommendations":[{"index":0,"reason":"matches"}]}`}
	opt := Options{Count: 3, CandidatePool: 10, MinLiked: 1, EmbeddingModel: "x"}

	res, err := Build(ctx, s, nil, NewSelector(fc), opt, "digest", nil)
	require.NoError(t, err)
	require.True(t, res.Vector)
	require.Equal(t, 2, res.Candidates)
	require.Len(t, res.Suggestions, 1)
	require.Equal(t, a, res.Suggestions[0].ID)
	require.Equal(t, "matches", res.Suggestions[0].Reason)

	// The picked link is persisted and excluded from later runs.
	res2, err := Build(ctx, s, nil, NewSelector(fc), opt, "digest", nil)
	require.NoError(t, err)
	require.Equal(t, 1, res2.Candidates, "already-recommended link excluded")
	require.Equal(t, b, res2.Suggestions[0].ID)
}

func TestBuild_LLMErrorFallsBackToTopN(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	liked := seed(t, ls, "https://e.com/liked", "liked", "article", intp(5))
	require.NoError(t, s.SetEmbedding(ctx, liked, axisVec(0)))
	a := seed(t, ls, "https://e.com/a", "close article", "article", nil)
	require.NoError(t, s.SetEmbedding(ctx, a, axisVec(0)))

	fc := &fakeSelectorChat{err: errors.New("boom")}
	opt := Options{Count: 3, CandidatePool: 10, MinLiked: 1, EmbeddingModel: "x"}

	res, err := Build(ctx, s, nil, NewSelector(fc), opt, "digest", nil)
	require.NoError(t, err)
	require.Len(t, res.Suggestions, 1)
	require.Equal(t, a, res.Suggestions[0].ID)
	require.Empty(t, res.Suggestions[0].Reason)
}

func TestBuild_NoRatingsTextFallbackDoesNotError(t *testing.T) {
	s, _ := newRecStore(t)
	ctx := context.Background()

	fc := &fakeSelectorChat{content: `{"recommendations":[]}`}
	opt := Options{Count: 3, CandidatePool: 10, MinLiked: 3, EmbeddingModel: "x"}

	res, err := Build(ctx, s, nil, NewSelector(fc), opt, "digest", nil)
	require.NoError(t, err)
	require.Empty(t, res.Suggestions)
	require.Zero(t, res.Candidates)
	require.False(t, res.Vector)
}

func TestBuild_IncludeSeen(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	liked := seed(t, ls, "https://e.com/liked", "liked", "article", intp(5))
	require.NoError(t, s.SetEmbedding(ctx, liked, axisVec(0)))
	a := seed(t, ls, "https://e.com/a", "close article", "article", nil)
	require.NoError(t, s.SetEmbedding(ctx, a, axisVec(0)))

	fc := &fakeSelectorChat{content: `{"recommendations":[{"index":0}]}`}
	base := Options{Count: 3, CandidatePool: 10, MinLiked: 1, EmbeddingModel: "x"}

	_, err := Build(ctx, s, nil, NewSelector(fc), base, "digest", nil)
	require.NoError(t, err)

	seen, err := Build(ctx, s, nil, NewSelector(fc), base, "chat", nil)
	require.NoError(t, err)
	require.Zero(t, seen.Candidates, "already recommended excluded by default")

	base.IncludeSeen = true
	again, err := Build(ctx, s, nil, NewSelector(fc), base, "chat", nil)
	require.NoError(t, err)
	require.Equal(t, 1, again.Candidates, "IncludeSeen allows previously-recommended links")
	require.Len(t, again.Suggestions, 1)
}

func TestBuild_SelectorNilFallsBack(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	liked := seed(t, ls, "https://e.com/liked", "liked", "article", intp(5))
	require.NoError(t, s.SetEmbedding(ctx, liked, axisVec(0)))
	a := seed(t, ls, "https://e.com/a", "close article", "article", nil)
	require.NoError(t, s.SetEmbedding(ctx, a, axisVec(0)))

	res, err := Build(ctx, s, nil, nil, Options{Count: 3, CandidatePool: 10, MinLiked: 1}, "digest", nil)
	require.NoError(t, err)
	require.Len(t, res.Suggestions, 1)
	require.Equal(t, a, res.Suggestions[0].ID)
	require.Empty(t, res.Suggestions[0].Reason)
}

type buildEmbedder struct{}

func (buildEmbedder) Embed(_ context.Context, _ string, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i := range inputs {
		out[i] = axisVec(i % 4)
	}
	return out, nil
}

func TestBuild_EmbedsPendingLinks(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	seed(t, ls, "https://e.com/a", "close article", "article", nil)

	res, err := Build(ctx, s, buildEmbedder{}, nil, Options{Count: 3, CandidatePool: 10}, "digest", nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Embedded)

	pending, err := s.PendingEmbeddings(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}
