// Package blogmarketing schedules Todoist marketing tasks for new posts on the
// user's blog. See docs/superpowers/specs/2026-05-07-blog-marketing-todoist-design.md
// and docs/superpowers/specs/2026-05-15-blog-marketing-reverse-sync-design.md.
package blogmarketing

import (
	"context"
	"fmt"
	"time"

	"darek/db"
)

// TaskRef identifies one of the 9 Todoist tasks created for a blog post,
// together with the cell (platform, cadence) it represents.
type TaskRef struct {
	Platform  Platform
	Cadence   Cadence
	TodoistID string
}

// Store is the Postgres-backed state for `blog_posts_scheduled` and the
// per-task `blog_post_tasks` join table.
type Store struct {
	pool *db.Pool
}

func NewStore(pool *db.Pool) *Store { return &Store{pool: pool} }

// Count returns the total number of scheduled-or-seen posts. Used by the
// orchestrator to detect "first-ever poll" mode (zero rows ⇒ backfill).
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM blog_posts_scheduled").Scan(&n); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}

// IsScheduled returns true if the canonical URL has any row at all. A
// "seen-only" row (scheduled_at IS NULL) still counts as scheduled — the point
// is dedup, not differentiating backfill from real schedules downstream.
func (s *Store) IsScheduled(ctx context.Context, canonicalURL string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM blog_posts_scheduled WHERE canonical_url = $1)",
		canonicalURL,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("is_scheduled: %w", err)
	}
	return exists, nil
}

// MarkSeenOnly inserts a parent row with scheduled_at=NULL and no task rows.
// Used by first-run backfill: the post is recorded as known so we don't
// re-schedule it later, but no Todoist tasks were created.
func (s *Store) MarkSeenOnly(ctx context.Context, canonicalURL string, publishedAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO blog_posts_scheduled (canonical_url, published_at)
		 VALUES ($1, $2)
		 ON CONFLICT (canonical_url) DO NOTHING`,
		canonicalURL, publishedAt,
	)
	if err != nil {
		return fmt.Errorf("mark_seen_only: %w", err)
	}
	return nil
}

// SaveTasks atomically inserts the parent `blog_posts_scheduled` row (with
// scheduled_at = now()) together with one `blog_post_tasks` row per ref.
// If the parent row already exists (re-poll race), the INSERT is a no-op and
// the task rows are still attempted; that path is not expected in normal use
// but is harmless because (canonical_url, platform, cadence) is the PK.
func (s *Store) SaveTasks(ctx context.Context, canonicalURL string, publishedAt time.Time, refs []TaskRef) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("save_tasks begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO blog_posts_scheduled (canonical_url, published_at, scheduled_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (canonical_url) DO NOTHING`,
		canonicalURL, publishedAt,
	); err != nil {
		return fmt.Errorf("save_tasks scheduled: %w", err)
	}

	platforms := make([]string, len(refs))
	cadences := make([]string, len(refs))
	ids := make([]string, len(refs))
	for i, r := range refs {
		platforms[i] = string(r.Platform)
		cadences[i] = string(r.Cadence)
		ids[i] = r.TodoistID
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO blog_post_tasks (canonical_url, platform, cadence, todoist_id)
		 SELECT $1, p, c, i
		 FROM unnest($2::text[], $3::text[], $4::text[]) AS x(p, c, i)`,
		canonicalURL, platforms, cadences, ids,
	); err != nil {
		return fmt.Errorf("save_tasks tasks: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("save_tasks commit: %w", err)
	}
	return nil
}

// GetTasks returns the task refs for a post in stable (platform, cadence)
// order. An empty slice with no error means the post exists as seen-only
// (or doesn't exist at all — callers should pair with IsScheduled if they
// need to distinguish).
func (s *Store) GetTasks(ctx context.Context, canonicalURL string) ([]TaskRef, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT platform, cadence, todoist_id
		 FROM blog_post_tasks
		 WHERE canonical_url = $1
		 ORDER BY platform, cadence`,
		canonicalURL,
	)
	if err != nil {
		return nil, fmt.Errorf("get_tasks query: %w", err)
	}
	defer rows.Close()
	var refs []TaskRef
	for rows.Next() {
		var p, c, id string
		if err := rows.Scan(&p, &c, &id); err != nil {
			return nil, fmt.Errorf("get_tasks scan: %w", err)
		}
		refs = append(refs, TaskRef{Platform: Platform(p), Cadence: Cadence(c), TodoistID: id})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get_tasks rows: %w", err)
	}
	return refs, nil
}

// LookupTask reverse-maps a Todoist task id back to the (post, platform,
// cadence) cell it belongs to. Returns pgx.ErrNoRows if the id is not one
// we created. Used by the future regenerate-label scanner.
func (s *Store) LookupTask(ctx context.Context, todoistID string) (canonicalURL string, platform Platform, cadence Cadence, err error) {
	var p, c string
	err = s.pool.QueryRow(ctx,
		`SELECT canonical_url, platform, cadence FROM blog_post_tasks WHERE todoist_id = $1`,
		todoistID,
	).Scan(&canonicalURL, &p, &c)
	if err != nil {
		return "", "", "", err
	}
	return canonicalURL, Platform(p), Cadence(c), nil
}
