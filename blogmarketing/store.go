// Package blogmarketing schedules Todoist marketing tasks for new posts on the
// user's blog. See docs/superpowers/specs/2026-05-07-blog-marketing-todoist-design.md.
package blogmarketing

import (
	"context"
	"fmt"
	"time"

	"darek/db"
)

// Store is the Postgres-backed state for `blog_posts_scheduled`.
type Store struct {
	pool *db.Pool
}

func NewStore(pool *db.Pool) *Store { return &Store{pool: pool} }

// Count returns the total number of rows. Used by the orchestrator to detect
// "first-ever poll" mode (zero rows ⇒ backfill).
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

// MarkSeenOnly inserts a row with scheduled_at=NULL and todoist_task_ids=NULL.
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

// MarkScheduled inserts a fully-scheduled row with the 9 Todoist task IDs.
func (s *Store) MarkScheduled(ctx context.Context, canonicalURL string, publishedAt time.Time, taskIDs []string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO blog_posts_scheduled (canonical_url, published_at, scheduled_at, todoist_task_ids)
		 VALUES ($1, $2, now(), $3)
		 ON CONFLICT (canonical_url) DO NOTHING`,
		canonicalURL, publishedAt, taskIDs,
	)
	if err != nil {
		return fmt.Errorf("mark_scheduled: %w", err)
	}
	return nil
}
