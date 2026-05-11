package exechistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"darek/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Store reads execution history from Postgres for the HTTP UI.
type Store struct {
	pool *db.Pool
}

func NewStore(pool *db.Pool) *Store { return &Store{pool: pool} }

// ListFilter controls Store.List. Before is a cursor on started_at (rows
// strictly before this time are returned); zero means "from now".
type ListFilter struct {
	Kind   string
	Before time.Time
	Limit  int
}

// ErrNotFound is returned by Get when no execution exists with the given id.
var ErrNotFound = errors.New("execution not found")

func (s *Store) List(ctx context.Context, f ListFilter) ([]Execution, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	args := []any{}
	where := ""
	if f.Kind != "" {
		args = append(args, f.Kind)
		where += fmt.Sprintf(" AND kind = $%d", len(args))
	}
	if !f.Before.IsZero() {
		args = append(args, f.Before)
		where += fmt.Sprintf(" AND started_at < $%d", len(args))
	}
	args = append(args, f.Limit)
	sql := fmt.Sprintf(`
SELECT id, trace_id, span_id, kind, name, started_at, ended_at, duration_ms, status, COALESCE(error,''), attributes
FROM executions
WHERE 1=1 %s
ORDER BY started_at DESC
LIMIT $%d`, where, len(args))

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Execution{}
	for rows.Next() {
		var e Execution
		var attrs []byte
		if err := rows.Scan(&e.ID, &e.TraceID, &e.SpanID, &e.Kind, &e.Name,
			&e.StartedAt, &e.EndedAt, &e.DurationMS, &e.Status, &e.Error, &attrs); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(attrs, &e.Attributes)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (Execution, []Step, error) {
	var e Execution
	var attrs []byte
	err := s.pool.QueryRow(ctx, `
SELECT id, trace_id, span_id, kind, name, started_at, ended_at, duration_ms, status, COALESCE(error,''), attributes
FROM executions WHERE id = $1`, id).
		Scan(&e.ID, &e.TraceID, &e.SpanID, &e.Kind, &e.Name,
			&e.StartedAt, &e.EndedAt, &e.DurationMS, &e.Status, &e.Error, &attrs)
	if errors.Is(err, pgx.ErrNoRows) {
		return Execution{}, nil, ErrNotFound
	}
	if err != nil {
		return Execution{}, nil, err
	}
	_ = json.Unmarshal(attrs, &e.Attributes)

	rows, err := s.pool.Query(ctx, `
SELECT id, execution_id, COALESCE(parent_span_id,''), span_id, name, started_at, ended_at, duration_ms, status, COALESCE(error,''), attributes, events
FROM execution_steps WHERE execution_id = $1 ORDER BY started_at ASC`, id)
	if err != nil {
		return Execution{}, nil, err
	}
	defer rows.Close()
	steps := []Step{}
	for rows.Next() {
		var sp Step
		var sa, se []byte
		if err := rows.Scan(&sp.ID, &sp.ExecutionID, &sp.ParentSpanID, &sp.SpanID, &sp.Name,
			&sp.StartedAt, &sp.EndedAt, &sp.DurationMS, &sp.Status, &sp.Error, &sa, &se); err != nil {
			return Execution{}, nil, err
		}
		_ = json.Unmarshal(sa, &sp.Attributes)
		_ = json.Unmarshal(se, &sp.Events)
		steps = append(steps, sp)
	}
	return e, steps, rows.Err()
}

func (s *Store) Kinds(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT kind FROM executions ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
