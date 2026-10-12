package recommend

import (
	"context"
	"math"
	"strings"
	"unicode"
)

// Profile is the user's taste derived from rated links.
type Profile struct {
	Liked            []Example
	Disliked         []Example
	LikedCentroid    []float32
	DislikedCentroid []float32
}

// BuildProfile loads the most recent liked (rating >= 4) and disliked
// (rating <= 2) examples and computes their centroids.
func BuildProfile(ctx context.Context, store *Store) (Profile, error) {
	liked, err := store.RatedExamples(ctx, 4, 5, 50)
	if err != nil {
		return Profile{}, err
	}
	disliked, err := store.RatedExamples(ctx, 1, 2, 50)
	if err != nil {
		return Profile{}, err
	}
	return Profile{
		Liked:            liked,
		Disliked:         disliked,
		LikedCentroid:    centroid(embeddings(liked)),
		DislikedCentroid: centroid(embeddings(disliked)),
	}, nil
}

// Candidates retrieves unrated links the user may want to read. It uses the
// pgvector path when there are enough embedded liked examples, and falls back
// to a full-text query built from recently-liked titles otherwise.
func Candidates(ctx context.Context, store *Store, p Profile, opt Options) ([]Candidate, bool, error) {
	perKind := opt.CandidatePool / 2
	if perKind <= 0 {
		perKind = 10
	}

	if p.LikedCentroid != nil && len(p.Liked) >= opt.MinLiked {
		cands, err := store.VectorCandidates(ctx, p.LikedCentroid, p.DislikedCentroid, opt.LookbackDays, opt.CooldownDays, perKind)
		if err != nil {
			return nil, false, err
		}
		if len(cands) > 0 {
			return cands, true, nil
		}
	}

	titles, err := store.LikedTitles(ctx, 50)
	if err != nil {
		return nil, false, err
	}
	q := textQuery(titles, 8)
	if q == "" {
		return nil, false, nil
	}
	cands, err := store.TextCandidates(ctx, q, opt.LookbackDays, opt.CooldownDays, perKind)
	return cands, false, err
}

// centroid averages the vectors and L2-normalizes the result. It returns nil
// when there are no (non-empty) vectors.
func centroid(vecs [][]float32) []float32 {
	if len(vecs) == 0 {
		return nil
	}
	dim := len(vecs[0])
	if dim == 0 {
		return nil
	}
	sum := make([]float64, dim)
	n := 0
	for _, v := range vecs {
		if len(v) != dim {
			continue
		}
		for i, f := range v {
			sum[i] += float64(f)
		}
		n++
	}
	if n == 0 {
		return nil
	}
	mean := make([]float32, dim)
	var norm float64
	for i := range sum {
		mean[i] = float32(sum[i] / float64(n))
		norm += float64(mean[i]) * float64(mean[i])
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return mean
	}
	for i := range mean {
		mean[i] = float32(float64(mean[i]) / norm)
	}
	return mean
}

func embeddings(exs []Example) [][]float32 {
	out := make([][]float32, 0, len(exs))
	for _, e := range exs {
		if len(e.Embedding) > 0 {
			out = append(out, e.Embedding)
		}
	}
	return out
}

// textQuery builds an OR-joined full-text query from titles, sanitizing tokens
// so they can't break to_tsquery syntax.
func textQuery(titles []string, max int) string {
	seen := map[string]struct{}{}
	var terms []string
	for _, title := range titles {
		for _, w := range strings.FieldsFunc(title, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			w = strings.ToLower(w)
			if len(w) < 3 {
				continue
			}
			if _, ok := seen[w]; ok {
				continue
			}
			seen[w] = struct{}{}
			terms = append(terms, w)
			if len(terms) >= max {
				return strings.Join(terms, " | ")
			}
		}
	}
	return strings.Join(terms, " | ")
}
