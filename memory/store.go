package memory

import (
	"context"
	"fmt"
	"time"

	"darek/db"
	"darek/obs"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Note struct {
	ID        uuid.UUID
	CreatedAt time.Time
	Body      string
	Tags      []string
	Source    string
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

func (s *Store) Save(ctx context.Context, body string, tags []string, source string) (uuid.UUID, error) {
	if body == "" {
		return uuid.Nil, fmt.Errorf("body required")
	}
	if source == "" {
		source = "user"
	}
	if tags == nil {
		tags = []string{}
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO notes (body, tags, source)
		VALUES ($1, $2, $3) RETURNING id
	`, body, tags, source).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert note: %w", err)
	}
	if s.m != nil {
		s.m.MemoryNotesSaved.Add(ctx, 1)
	}
	return id, nil
}

// Recall returns up to limit notes ranked by tsvector match against query.
// Empty query → most recent notes.
func (s *Store) Recall(ctx context.Context, query string, limit int) ([]Note, error) {
	if limit <= 0 {
		limit = 5
	}
	var (
		out []Note
		cur pgx.Rows
		err error
	)
	if query == "" {
		cur, err = s.pool.Query(ctx, `
			SELECT id, created_at, body, tags, source
			FROM notes
			ORDER BY created_at DESC
			LIMIT $1
		`, limit)
	} else {
		cur, err = s.pool.Query(ctx, `
			SELECT id, created_at, body, tags, source
			FROM notes
			WHERE search @@ plainto_tsquery('simple', $1)
			ORDER BY ts_rank(search, plainto_tsquery('simple', $1)) DESC, created_at DESC
			LIMIT $2
		`, query, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer cur.Close()
	for cur.Next() {
		var n Note
		if err := cur.Scan(&n.ID, &n.CreatedAt, &n.Body, &n.Tags, &n.Source); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, n)
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	if s.m != nil {
		s.m.MemoryNotesRecalled.Add(ctx, int64(len(out)))
	}
	return out, nil
}
