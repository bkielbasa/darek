package links

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Link struct {
	ID        uuid.UUID
	URL       string
	Title     string
	Rating    *int // nil = unrated
	Tags      []string
	Notes     string
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type SaveInput struct {
	URL         string
	Title       string
	Rating      *int     // 1..5 or nil; nil leaves existing rating untouched on update
	Tags        []string
	Notes       string
	Source      string // defaults to "user"
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
	err = tx.QueryRow(ctx, `SELECT id FROM links WHERE url = $1`, in.URL).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Insert
		err = tx.QueryRow(ctx, `
			INSERT INTO links (url, title, rating, tags, notes, source)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id
		`, in.URL, in.Title, in.Rating, in.Tags, in.Notes, in.Source).Scan(&id)
		if err != nil {
			return uuid.Nil, fmt.Errorf("insert: %w", err)
		}
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
		_, err = tx.Exec(ctx, `UPDATE links SET `+strings.Join(set, ", ")+` WHERE id = $1`, args...)
		if err != nil {
			return uuid.Nil, fmt.Errorf("update: %w", err)
		}
	}
	return id, tx.Commit(ctx)
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
	if !o.Since.IsZero() {
		args = append(args, o.Since)
		conds = append(conds, fmt.Sprintf("created_at >= $%d", len(args)))
	}
	args = append(args, o.Limit)

	q := `
		SELECT id, url, coalesce(title,''), rating, tags, coalesce(notes,''), source, created_at, updated_at
		FROM links
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY ` + orderBy(o) + `
		LIMIT $` + fmt.Sprint(len(args))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	return scanLinks(rows)
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
		SELECT id, url, coalesce(title,''), rating, tags, coalesce(notes,''), source, created_at, updated_at,
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
		if err := rows.Scan(&l.ID, &l.URL, &l.Title, &l.Rating, &l.Tags, &l.Notes, &l.Source, &l.CreatedAt, &l.UpdatedAt, &rank); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
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
		if err := rows.Scan(&l.ID, &l.URL, &l.Title, &l.Rating, &l.Tags, &l.Notes, &l.Source, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
