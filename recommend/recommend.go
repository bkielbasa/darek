package recommend

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"darek/obs"
)

// Options controls a recommendation run.
type Options struct {
	Count          int
	CandidatePool  int
	LookbackDays   int
	CooldownDays   int
	MinLiked       int
	EmbeddingModel string
	EmbedLimit     int
	EmbedBatch     int
	// Kind optionally restricts candidates to one kind ("article"|"video").
	Kind string
	// IncludeSeen allows already-recommended links back into the candidate
	// pool (ignores the never-repeat filter).
	IncludeSeen bool
}

// Result summarizes a recommendation run.
type Result struct {
	Suggestions []Suggestion
	Embedded    int
	Candidates  int
	Vector      bool
}

// Build runs the full recommendation pipeline: embed any pending links, build
// the taste profile, retrieve candidates, pick with the LLM (falling back to
// the top-scored candidates), persist the picks and record metrics. DB errors
// are returned; embedding/LLM/save failures are logged and tolerated.
func Build(ctx context.Context, store *Store, emb Embedder, sel *Selector, opt Options, source string, logf func(string, ...any)) (Result, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var res Result

	if emb != nil {
		n, err := EnsureEmbeddings(ctx, store, emb, opt.EmbeddingModel, opt.EmbedLimit, opt.EmbedBatch)
		if err != nil {
			logf("recommend: ensure embeddings: %v", err)
		}
		res.Embedded = n
	}

	p, err := BuildProfile(ctx, store)
	if err != nil {
		return res, err
	}

	cands, usedVector, err := Candidates(ctx, store, p, opt)
	if err != nil {
		return res, err
	}
	res.Candidates = len(cands)
	res.Vector = usedVector
	if len(cands) == 0 {
		return res, nil
	}

	count := opt.Count
	if count <= 0 {
		count = 3
	}

	var picks []Suggestion
	if sel != nil {
		picks, err = sel.Select(ctx, p, cands, count)
		if err != nil {
			logf("recommend: selector failed, using top candidates: %v", err)
		}
	}
	if len(picks) == 0 {
		picks = topN(cands, count)
	}

	if err := store.SaveRecommendations(ctx, picks, source); err != nil {
		logf("recommend: save recommendations: %v", err)
	}

	res.Suggestions = picks
	recordRecommend(ctx, source, res)
	return res, nil
}

// topN returns the first n candidates with no reason, used as a fallback when
// the LLM selector is unavailable or picks nothing.
func topN(cands []Candidate, n int) []Suggestion {
	if n > len(cands) {
		n = len(cands)
	}
	out := make([]Suggestion, 0, n)
	for _, c := range cands[:n] {
		out = append(out, Suggestion{Candidate: c})
	}
	return out
}

func recordRecommend(ctx context.Context, source string, res Result) {
	m, _ := obs.MetricsInstance()
	if m == nil {
		return
	}
	attrs := metric.WithAttributes(attribute.String("source", source))
	m.RecommendRuns.Add(ctx, 1, attrs)
	m.RecommendSuggestions.Add(ctx, int64(len(res.Suggestions)), attrs)
	m.RecommendEmbeddings.Add(ctx, int64(res.Embedded))
}
