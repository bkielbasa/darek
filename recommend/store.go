// Package recommend builds rating-driven reading suggestions from the links
// table: it embeds unread links, derives a taste profile from rated examples,
// retrieves candidates via pgvector cosine similarity (with a full-text
// fallback), re-ranks them with an LLM, and records what was suggested so it is
// never repeated.
package recommend

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"darek/db"

	"github.com/google/uuid"
)

// embeddingTextExpr is the concatenation of a link's textual signal, used both
// to build the input to the embedding model and to filter out empty rows.
const embeddingTextExpr = `coalesce(title,'') || ' ' || coalesce(summary,'') || ' ' ||
	immutable_array_to_string(tags, ' ') || ' ' || coalesce(feed,'')`

// EmbedText is a link awaiting (or being) embedded.
type EmbedText struct {
	ID   uuid.UUID
	Text string
}

// Example is a rated link used to build the taste profile.
type Example struct {
	ID        uuid.UUID
	Title     string
	Kind      string
	Feed      string
	Summary   string
	Rating    int
	Embedding []float32
}

// Candidate is an unrated link retrieved as a possible recommendation.
type Candidate struct {
	ID      uuid.UUID
	URL     string
	Title   string
	Kind    string
	Feed    string
	Summary string
	Score   float32
}

// Suggestion is a candidate chosen to be shown, with the model's reasoning.
type Suggestion struct {
	Candidate
	Reason string
}

// Store provides database access for the recommender.
type Store struct {
	pool *db.Pool
}

// NewStore constructs a Store over the given pool.
func NewStore(pool *db.Pool) *Store { return &Store{pool: pool} }

// PendingEmbeddings returns links that have no embedding yet, newest first.
func (s *Store) PendingEmbeddings(ctx context.Context, limit int) ([]EmbedText, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, btrim(regexp_replace(`+embeddingTextExpr+`, '\s+', ' ', 'g')) AS text
		  FROM links
		 WHERE embedded_at IS NULL
		   AND (`+embeddingTextExpr+`) <> ''
		 ORDER BY created_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EmbedText
	for rows.Next() {
		var e EmbedText
		if err := rows.Scan(&e.ID, &e.Text); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetEmbedding stores a link's vector and marks it embedded.
func (s *Store) SetEmbedding(ctx context.Context, id uuid.UUID, vec []float32) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE links
		   SET embedding = CAST($2::text AS vector), embedded_at = now()
		 WHERE id = $1`, id, vectorString(vec))
	return err
}

// RatedExamples returns links rated within [minRating,maxRating] that have an
// embedding, most recent first.
func (s *Store) RatedExamples(ctx context.Context, minRating, maxRating, limit int) ([]Example, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, coalesce(title,''), kind, coalesce(feed,''), coalesce(summary,''),
		       rating, embedding::text
		  FROM links
		 WHERE rating IS NOT NULL
		   AND rating >= $1
		   AND rating <= $2
		   AND embedding IS NOT NULL
		 ORDER BY created_at DESC
		 LIMIT $3`, minRating, maxRating, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Example
	for rows.Next() {
		var (
			e   Example
			vec string
		)
		if err := rows.Scan(&e.ID, &e.Title, &e.Kind, &e.Feed, &e.Summary, &e.Rating, &vec); err != nil {
			return nil, err
		}
		emb, err := scanVector(vec)
		if err != nil {
			return nil, err
		}
		e.Embedding = emb
		out = append(out, e)
	}
	return out, rows.Err()
}

// VectorCandidates retrieves unrated, embedded links closest to liked (and
// farthest from disliked) per kind, interleaving articles and videos.
func (s *Store) VectorCandidates(ctx context.Context, liked, disliked []float32, lookbackDays, cooldownDays, perKind int) ([]Candidate, error) {
	if perKind <= 0 {
		perKind = 10
	}
	arts, err := s.vectorKind(ctx, "article", liked, disliked, lookbackDays, cooldownDays, perKind)
	if err != nil {
		return nil, err
	}
	vids, err := s.vectorKind(ctx, "video", liked, disliked, lookbackDays, cooldownDays, perKind)
	if err != nil {
		return nil, err
	}
	return interleave(arts, vids, perKind*2), nil
}

func (s *Store) vectorKind(ctx context.Context, kind string, liked, disliked []float32, lookbackDays, cooldownDays, perKind int) ([]Candidate, error) {
	var args []any
	args = append(args, vectorString(liked))
	likedIdx := len(args)

	var scoreExpr string
	if len(disliked) > 0 {
		args = append(args, vectorString(disliked))
		disIdx := len(args)
		scoreExpr = fmt.Sprintf("(embedding <=> CAST($%d::text AS vector)) - (embedding <=> CAST($%d::text AS vector))", disIdx, likedIdx)
	} else {
		scoreExpr = fmt.Sprintf("1 - (embedding <=> CAST($%d::text AS vector))", likedIdx)
	}

	args = append(args, kind)
	kindIdx := len(args)

	filter := ""
	if lookbackDays > 0 {
		filter += lookbackClause(&args, lookbackDays)
	}
	filter += cooldownClause(&args, cooldownDays)

	args = append(args, perKind)
	limitIdx := len(args)

	q := fmt.Sprintf(`
		SELECT id, url, coalesce(title,''), kind, coalesce(feed,''), coalesce(summary,''), %s AS score
		  FROM links
		 WHERE kind = $%d
		   AND rating IS NULL
		   AND embedding IS NOT NULL%s
		 ORDER BY score DESC
		 LIMIT $%d`, scoreExpr, kindIdx, filter, limitIdx)

	return s.scanCandidates(ctx, q, args)
}

// TextCandidates is the fallback path used when there aren't enough liked
// examples to build a reliable embedding centroid. It ranks unrated links by
// full-text relevance to query, per kind, interleaved.
func (s *Store) TextCandidates(ctx context.Context, query string, lookbackDays, cooldownDays, perKind int) ([]Candidate, error) {
	if perKind <= 0 {
		perKind = 10
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	arts, err := s.textKind(ctx, "article", query, lookbackDays, cooldownDays, perKind)
	if err != nil {
		return nil, err
	}
	vids, err := s.textKind(ctx, "video", query, lookbackDays, cooldownDays, perKind)
	if err != nil {
		return nil, err
	}
	return interleave(arts, vids, perKind*2), nil
}

func (s *Store) textKind(ctx context.Context, kind, query string, lookbackDays, cooldownDays, perKind int) ([]Candidate, error) {
	args := []any{query}
	filter := ""
	if lookbackDays > 0 {
		filter += lookbackClause(&args, lookbackDays)
	}
	filter += cooldownClause(&args, cooldownDays)

	args = append(args, kind)
	kindIdx := len(args)
	args = append(args, perKind)
	limitIdx := len(args)

	q := fmt.Sprintf(`
		SELECT id, url, coalesce(title,''), kind, coalesce(feed,''), coalesce(summary,''),
		       ts_rank(search, plainto_tsquery('simple', $1)) AS score
		  FROM links
		 WHERE search @@ plainto_tsquery('simple', $1)
		   AND kind = $%d
		   AND rating IS NULL%s
		 ORDER BY score DESC
		 LIMIT $%d`, kindIdx, filter, limitIdx)

	return s.scanCandidates(ctx, q, args)
}

// SaveRecommendations records suggestions so they are not repeated. Existing
// rows for the same link are left untouched.
func (s *Store) SaveRecommendations(ctx context.Context, picks []Suggestion, surface string) error {
	if len(picks) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, p := range picks {
		if _, err := tx.Exec(ctx, `
			INSERT INTO link_recommendations (link_id, score, reason, surface)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (link_id) DO NOTHING`, p.ID, p.Score, p.Reason, surface); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) scanCandidates(ctx context.Context, q string, args []any) ([]Candidate, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.ID, &c.URL, &c.Title, &c.Kind, &c.Feed, &c.Summary, &c.Score); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func lookbackClause(args *[]any, days int) string {
	*args = append(*args, days)
	return fmt.Sprintf(" AND created_at > now() - make_interval(days => $%d::int)", len(*args))
}

// cooldownClause excludes links already recommended. cooldownDays<=0 means
// "never repeat"; a positive value only excludes recent suggestions.
func cooldownClause(args *[]any, cooldownDays int) string {
	if cooldownDays <= 0 {
		return " AND NOT EXISTS (SELECT 1 FROM link_recommendations r WHERE r.link_id = links.id)"
	}
	*args = append(*args, cooldownDays)
	return fmt.Sprintf(` AND NOT EXISTS (
		SELECT 1 FROM link_recommendations r
		 WHERE r.link_id = links.id
		   AND r.created_at > now() - make_interval(days => $%d::int))`, len(*args))
}

// vectorString renders a vector as the text form pgvector accepts ("[1,2,3]").
func vectorString(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// scanVector parses pgvector's text form ("[1,2,3]") into a float32 slice.
func scanVector(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, fmt.Errorf("parse vector component %q: %w", p, err)
		}
		out[i] = float32(f)
	}
	return out, nil
}

// interleave alternates items from a and b until max is reached.
func interleave(a, b []Candidate, max int) []Candidate {
	out := make([]Candidate, 0, len(a)+len(b))
	for i := 0; i < len(a) || i < len(b); i++ {
		if i < len(a) {
			out = append(out, a[i])
		}
		if i < len(b) {
			out = append(out, b[i])
		}
		if max > 0 && len(out) >= max {
			return out[:max]
		}
	}
	return out
}
