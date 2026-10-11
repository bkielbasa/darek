package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Candidate is one memory proposed by the extractor. DedupeKey is the stable
// identity of the memory; an empty key is derived from kind + summary.
type Candidate struct {
	Kind      string
	Summary   string
	Details   string
	People    []string
	DueAt     *time.Time
	DedupeKey string
}

// Memory is a persisted structured memory row.
type Memory struct {
	ID         uuid.UUID
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastSeenAt time.Time
	Kind       string
	Summary    string
	Details    string
	People     []string
	Groups     []string
	DueAt      *time.Time
	Status     string
	DedupeKey  string
	Source     string
	SourceRefs []string
}

const memoryColumns = `id, created_at, updated_at, last_seen_at, kind, summary, details,
	people, groups, due_at, status, dedupe_key, source, source_refs`

// slug lowercases s and collapses every run of non-alphanumeric characters to a
// single "-". ASCII-only: non-ASCII letters are separators, so name variants can
// collide — acceptable for a personal system.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// dedupeKey normalizes the candidate's key, deriving one when absent.
func dedupeKey(c Candidate) string {
	if k := slug(c.DedupeKey); k != "" {
		return k
	}
	return slug(c.Kind + "-" + c.Summary)
}

func collectMemories(rows pgx.Rows) ([]Memory, error) {
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt, &m.LastSeenAt,
			&m.Kind, &m.Summary, &m.Details, &m.People, &m.Groups, &m.DueAt,
			&m.Status, &m.DedupeKey, &m.Source, &m.SourceRefs); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveMemories upserts candidates by dedupe_key. Returns the number of rows
// written. On conflict it refreshes summary/details/last_seen_at, unions
// groups + source_refs, COALESCEs due_at, and preserves status.
func (s *Store) SaveMemories(ctx context.Context, cands []Candidate, group string, sourceRefs []string) (int, error) {
	if len(cands) == 0 {
		return 0, nil
	}
	if sourceRefs == nil {
		sourceRefs = []string{}
	}
	groups := []string{}
	if group != "" {
		groups = []string{group}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	saved := 0
	for _, c := range cands {
		people := c.People
		if people == nil {
			people = []string{}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO memories
			    (kind, summary, details, people, groups, due_at, dedupe_key, source, source_refs)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'digest',$8)
			ON CONFLICT (dedupe_key) DO UPDATE SET
			    updated_at   = now(),
			    last_seen_at = now(),
			    summary      = EXCLUDED.summary,
			    details      = EXCLUDED.details,
			    people       = EXCLUDED.people,
			    groups       = (SELECT array(SELECT DISTINCT unnest(memories.groups || EXCLUDED.groups))),
			    source_refs  = (SELECT array(SELECT DISTINCT unnest(memories.source_refs || EXCLUDED.source_refs))),
			    due_at       = COALESCE(EXCLUDED.due_at, memories.due_at)
		`, c.Kind, c.Summary, c.Details, people, groups, c.DueAt, dedupeKey(c), sourceRefs)
		if err != nil {
			return saved, fmt.Errorf("upsert memory %q: %w", dedupeKey(c), err)
		}
		saved++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	if s.m != nil {
		s.m.MemoriesSaved.Add(ctx, int64(saved))
	}
	return saved, nil
}

// ActiveItems returns active agreements/events due before now+horizonDays, or
// undated but seen within recentDays. Ordered due_at ASC NULLS LAST, then recency.
func (s *Store) ActiveItems(ctx context.Context, horizonDays, recentDays, limit int) ([]Memory, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+memoryColumns+`
		  FROM memories
		 WHERE status = 'active'
		   AND kind IN ('agreement', 'event')
		   AND ((due_at IS NOT NULL AND due_at < now() + make_interval(days => $1))
		        OR (due_at IS NULL AND last_seen_at >= now() - make_interval(days => $2)))
		 ORDER BY due_at ASC NULLS LAST, last_seen_at DESC
		 LIMIT $3
	`, horizonDays, recentDays, limit)
	if err != nil {
		return nil, fmt.Errorf("query active: %w", err)
	}
	return collectMemories(rows)
}

// PersonFacts returns active person_fact rows seen within the last sinceDays.
func (s *Store) PersonFacts(ctx context.Context, sinceDays, limit int) ([]Memory, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+memoryColumns+`
		  FROM memories
		 WHERE status = 'active'
		   AND kind = 'person_fact'
		   AND last_seen_at >= now() - make_interval(days => $1)
		 ORDER BY last_seen_at DESC
		 LIMIT $2
	`, sinceDays, limit)
	if err != nil {
		return nil, fmt.Errorf("query people: %w", err)
	}
	return collectMemories(rows)
}

// ListMemories is the chat-recall query. Empty query/kind/status are wildcards.
func (s *Store) ListMemories(ctx context.Context, query, kind, status string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 8
	}
	var (
		conds []string
		args  []any
	)
	if query != "" {
		args = append(args, query)
		conds = append(conds, fmt.Sprintf("search @@ plainto_tsquery('simple', $%d)", len(args)))
	}
	if kind != "" {
		args = append(args, kind)
		conds = append(conds, fmt.Sprintf("kind = $%d", len(args)))
	}
	if status != "" {
		args = append(args, status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM memories
		%s
		ORDER BY last_seen_at DESC
		LIMIT $%d
	`, memoryColumns, where, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("query list: %w", err)
	}
	return collectMemories(rows)
}

// SetMemoryStatus updates one memory's status. No caller exposes this in v1.
func (s *Store) SetMemoryStatus(ctx context.Context, id uuid.UUID, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE memories SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	return err
}
