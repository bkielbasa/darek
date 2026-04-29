package links

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"darek/db"
	"darek/obs"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type Link struct {
	ID        uuid.UUID
	URL       string
	Title     string
	Rating    *int // nil = unrated
	Tags      []string
	Notes     string
	Source    string
	Kind      string // "article" | "video" | "tweet" | "podcast" | "other"
	Feed      string // RSS feed name; empty for non-RSS sources
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Store struct {
	pool *db.Pool
	m    *obs.Metrics
}

func NewStore(pool *db.Pool) *Store {
	var m *obs.Metrics
	if got, err := obs.MetricsInstance(); err == nil {
		m = got
	}
	return &Store{pool: pool, m: m}
}

type SaveInput struct {
	URL         string
	Title       string
	Rating      *int     // 1..5 or nil
	Tags        []string
	Notes       string
	Source      string // defaults to "user"
	Kind        string // defaults to "article" on insert; ignored on update if empty
	Feed        string // RSS feed name; empty leaves existing intact on update
	ReplaceTags bool   // false (default): merge; true: overwrite
}

// Save upserts a link by URL. Provided non-empty fields update existing rows;
// empty fields leave the existing values intact.
func (s *Store) Save(ctx context.Context, in SaveInput) (uuid.UUID, error) {
	if in.URL == "" {
		return uuid.Nil, fmt.Errorf("url required")
	}
	if in.Rating != nil && (*in.Rating < 1 || *in.Rating > 5) {
		return uuid.Nil, fmt.Errorf("rating must be 1..5")
	}
	in.Tags = lowercaseSet(in.Tags)
	if in.Source == "" {
		in.Source = "user"
	}

	// Approach: try INSERT; on conflict, do an UPDATE that respects "merge tags"
	// and "leave existing fields intact when input is empty". A single-statement
	// upsert is awkward with conditional merge; do this in two steps inside a tx.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	var id uuid.UUID
	var op string
	err = tx.QueryRow(ctx, `SELECT id FROM links WHERE url = $1`, in.URL).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Insert
		kind := in.Kind
		if kind == "" {
			kind = "article"
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO links (url, title, rating, tags, notes, source, kind, feed)
			VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''))
			RETURNING id
		`, in.URL, in.Title, in.Rating, in.Tags, in.Notes, in.Source, kind, in.Feed).Scan(&id)
		if err != nil {
			return uuid.Nil, fmt.Errorf("insert: %w", err)
		}
		op = "save_new"
	case err != nil:
		return uuid.Nil, fmt.Errorf("lookup: %w", err)
	default:
		// Update: COALESCE-style for nullable/empty fields; merge or replace tags.
		args := []any{id}
		set := []string{"updated_at = now()"}
		if in.Title != "" {
			args = append(args, in.Title)
			set = append(set, fmt.Sprintf("title = $%d", len(args)))
		}
		if in.Rating != nil {
			args = append(args, *in.Rating)
			set = append(set, fmt.Sprintf("rating = $%d", len(args)))
		}
		if in.Notes != "" {
			args = append(args, in.Notes)
			set = append(set, fmt.Sprintf("notes = $%d", len(args)))
		}
		if len(in.Tags) > 0 {
			args = append(args, in.Tags)
			if in.ReplaceTags {
				set = append(set, fmt.Sprintf("tags = $%d", len(args)))
			} else {
				// Merge: union of arrays, deduped.
				set = append(set, fmt.Sprintf(
					`tags = ARRAY(SELECT DISTINCT unnest(tags || $%d::text[]))`, len(args)))
			}
		}
		if in.Kind != "" {
			args = append(args, in.Kind)
			set = append(set, fmt.Sprintf("kind = $%d", len(args)))
		}
		if in.Feed != "" {
			args = append(args, in.Feed)
			set = append(set, fmt.Sprintf("feed = $%d", len(args)))
		}
		_, err = tx.Exec(ctx, `UPDATE links SET `+strings.Join(set, ", ")+` WHERE id = $1`, args...)
		if err != nil {
			return uuid.Nil, fmt.Errorf("update: %w", err)
		}
		op = "save_update"
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	if s.m != nil {
		s.m.LinksEvents.Add(ctx, 1, metric.WithAttributes(attribute.String("op", op)))
	}
	return id, nil
}

// Delete removes a link by id (or by url if id is uuid.Nil).
func (s *Store) Delete(ctx context.Context, id uuid.UUID, url string) error {
	if id != uuid.Nil {
		_, err := s.pool.Exec(ctx, `DELETE FROM links WHERE id = $1`, id)
		return err
	}
	if url != "" {
		_, err := s.pool.Exec(ctx, `DELETE FROM links WHERE url = $1`, url)
		return err
	}
	return fmt.Errorf("id or url required")
}

type SearchOpts struct {
	Query     string
	MinRating int      // 0 = no constraint
	Tags      []string // ALL of these required (lowercased)
	Source    string   // optional filter
	Kind      string   // optional filter ("article"|"video"|"tweet"|"podcast"|"other")
	Feed      string   // optional filter — exact match
	Since     time.Time
	Limit     int
}

func (s *Store) Search(ctx context.Context, o SearchOpts) ([]Link, error) {
	if o.Limit <= 0 {
		o.Limit = 20
	}
	o.Tags = lowercaseSet(o.Tags)

	conds := []string{"TRUE"}
	args := []any{}
	if o.Query != "" {
		args = append(args, o.Query)
		conds = append(conds, fmt.Sprintf("search @@ plainto_tsquery('simple', $%d)", len(args)))
	}
	if o.MinRating > 0 {
		args = append(args, o.MinRating)
		conds = append(conds, fmt.Sprintf("rating >= $%d", len(args)))
	}
	if len(o.Tags) > 0 {
		args = append(args, o.Tags)
		conds = append(conds, fmt.Sprintf("tags @> $%d", len(args)))
	}
	if o.Source != "" {
		args = append(args, o.Source)
		conds = append(conds, fmt.Sprintf("source = $%d", len(args)))
	}
	if o.Kind != "" {
		args = append(args, o.Kind)
		conds = append(conds, fmt.Sprintf("kind = $%d", len(args)))
	}
	if o.Feed != "" {
		args = append(args, o.Feed)
		conds = append(conds, fmt.Sprintf("feed = $%d", len(args)))
	}
	if !o.Since.IsZero() {
		args = append(args, o.Since)
		conds = append(conds, fmt.Sprintf("created_at >= $%d", len(args)))
	}
	args = append(args, o.Limit)

	q := `
		SELECT id, url, coalesce(title,''), rating, tags, coalesce(notes,''), source, kind, coalesce(feed,''), created_at, updated_at
		FROM links
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY ` + orderBy(o) + `
		LIMIT $` + fmt.Sprint(len(args))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	out, err := scanLinks(rows)
	if err != nil {
		return nil, err
	}
	if s.m != nil {
		s.m.LinksEvents.Add(ctx, 1, metric.WithAttributes(attribute.String("op", "search")))
	}
	return out, nil
}

// Similar returns links ranked by tsvector similarity to the given free-form text,
// skipping unrated links (so the agent only sees signal-bearing examples).
func (s *Store) Similar(ctx context.Context, text string, limit int) ([]Link, error) {
	if text == "" {
		return nil, fmt.Errorf("text required")
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, url, coalesce(title,''), rating, tags, coalesce(notes,''), source, kind, coalesce(feed,''), created_at, updated_at,
		       ts_rank(search, plainto_tsquery('simple', $1)) AS rank
		FROM links
		WHERE rating IS NOT NULL
		  AND search @@ plainto_tsquery('simple', $1)
		ORDER BY rank DESC, rating DESC NULLS LAST, created_at DESC
		LIMIT $2
	`, text, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		var rank float32
		if err := rows.Scan(&l.ID, &l.URL, &l.Title, &l.Rating, &l.Tags, &l.Notes, &l.Source, &l.Kind, &l.Feed, &l.CreatedAt, &l.UpdatedAt, &rank); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if s.m != nil {
		s.m.LinksEvents.Add(ctx, 1, metric.WithAttributes(attribute.String("op", "similar")))
	}
	return out, nil
}

func orderBy(o SearchOpts) string {
	if o.Query != "" {
		return "ts_rank(search, plainto_tsquery('simple', '" + escapeForOrder(o.Query) + "')) DESC, rating DESC NULLS LAST, created_at DESC"
	}
	return "rating DESC NULLS LAST, created_at DESC"
}

// Conservative: strip single quotes since the query is folded into the ORDER BY.
// Args are still sent positionally for the WHERE; this is just for ts_rank in ORDER BY.
// (Alternative: pass query twice — but that complicates the dynamic args slice.)
func escapeForOrder(q string) string { return strings.ReplaceAll(q, "'", "") }

func lowercaseSet(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func scanLinks(rows pgx.Rows) ([]Link, error) {
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.URL, &l.Title, &l.Rating, &l.Tags, &l.Notes, &l.Source, &l.Kind, &l.Feed, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ErrNoRows returns pgx.ErrNoRows so consumers can compare without importing pgx.
func ErrNoRows() error { return pgx.ErrNoRows }

// Pool returns the underlying *db.Pool. Used by callers that need to issue
// statements not covered by Save/Search/Similar (e.g. clearing fields where
// "nil means leave alone" semantics block them).
func (s *Store) Pool() *db.Pool { return s.pool }

// Get returns a single link by id.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (Link, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, url, coalesce(title,''), rating, tags, coalesce(notes,''), source, kind, coalesce(feed,''), created_at, updated_at
		FROM links WHERE id = $1
	`, id)
	if err != nil {
		return Link{}, err
	}
	defer rows.Close()
	got, err := scanLinks(rows)
	if err != nil {
		return Link{}, err
	}
	if len(got) == 0 {
		return Link{}, fmt.Errorf("link %s not found", id)
	}
	return got[0], nil
}
