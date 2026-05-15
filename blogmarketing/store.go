// Package blogmarketing schedules Todoist marketing tasks for new posts on the
// user's blog. See docs/superpowers/specs/2026-05-07-blog-marketing-todoist-design.md
// and docs/superpowers/specs/2026-05-15-blog-marketing-reverse-sync-design.md.
package blogmarketing

import (
	"context"
	"fmt"
	"time"

	"darek/db"
	"darek/tools/blogfeed"
)

// ErrEntryMetaMissing is returned by GetEntry when a row pre-dates the
// 0014 migration — entry_url / entry_title / entry_summary are NULL. The
// regenerate orchestrator surfaces this so the user knows to either
// re-schedule the campaign or fix the task by hand.
var ErrEntryMetaMissing = fmt.Errorf("blog post entry meta not captured (pre-0014 row)")

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

// Count returns the number of scheduled-or-seen posts for one blog. Used by
// the orchestrator to detect per-blog "first-ever poll" mode (zero rows for
// THIS blog_id ⇒ backfill) — adding a new blog later does NOT spawn campaigns
// for its existing back-catalog.
func (s *Store) Count(ctx context.Context, blogID string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM blog_posts_scheduled WHERE blog_id = $1",
		blogID,
	).Scan(&n); err != nil {
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
// re-schedule it later, but no Todoist tasks were created. Entry meta
// (url/title/summary) is persisted so a later regenerate run can re-draft
// even if the post has aged out of the RSS feed.
func (s *Store) MarkSeenOnly(ctx context.Context, e blogfeed.Entry, blogID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO blog_posts_scheduled (canonical_url, blog_id, published_at, entry_url, entry_title, entry_summary)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (canonical_url) DO NOTHING`,
		e.CanonicalURL, blogID, e.PublishedAt, e.URL, e.Title, e.Summary,
	)
	if err != nil {
		return fmt.Errorf("mark_seen_only: %w", err)
	}
	return nil
}

// SaveTasks atomically inserts the parent `blog_posts_scheduled` row (with
// scheduled_at = now() AND entry meta) together with one `blog_post_tasks`
// row per ref. If the parent row already exists (re-poll race), the INSERT
// is a no-op and the task rows are still attempted; that path is not expected
// in normal use but is harmless because (canonical_url, platform, cadence)
// is the PK.
func (s *Store) SaveTasks(ctx context.Context, e blogfeed.Entry, blogID string, refs []TaskRef) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("save_tasks begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO blog_posts_scheduled (canonical_url, blog_id, published_at, scheduled_at, entry_url, entry_title, entry_summary)
		 VALUES ($1, $2, $3, now(), $4, $5, $6)
		 ON CONFLICT (canonical_url) DO NOTHING`,
		e.CanonicalURL, blogID, e.PublishedAt, e.URL, e.Title, e.Summary,
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
		e.CanonicalURL, platforms, cadences, ids,
	); err != nil {
		return fmt.Errorf("save_tasks tasks: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("save_tasks commit: %w", err)
	}
	return nil
}

// GetEntry returns the blog entry meta captured at scheduling time. Returns
// pgx.ErrNoRows if the canonical URL is unknown, and ErrEntryMetaMissing if
// the row exists but pre-dates the 0014 migration (entry_url/title/summary
// are NULL).
func (s *Store) GetEntry(ctx context.Context, canonicalURL string) (*blogfeed.Entry, error) {
	var (
		publishedAt time.Time
		entryURL    *string
		title       *string
		summary     *string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT published_at, entry_url, entry_title, entry_summary
		 FROM blog_posts_scheduled
		 WHERE canonical_url = $1`,
		canonicalURL,
	).Scan(&publishedAt, &entryURL, &title, &summary)
	if err != nil {
		return nil, err
	}
	if entryURL == nil || title == nil {
		return nil, ErrEntryMetaMissing
	}
	e := &blogfeed.Entry{
		CanonicalURL: canonicalURL,
		URL:          *entryURL,
		Title:        *title,
		PublishedAt:  publishedAt,
	}
	if summary != nil {
		e.Summary = *summary
	}
	return e, nil
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

// TaskState is everything we know in our DB about one scheduled Todoist task.
// PostedAt is nil if the auto-poster hasn't successfully published this cell;
// non-nil means the post was sent (and Todoist might or might not be marked
// completed yet — that's a separate concern).
type TaskState struct {
	CanonicalURL string
	BlogID       string
	Platform     Platform
	Cadence      Cadence
	TodoistID    string
	PostedAt     *time.Time
	PostedURL    string
}

// GetTaskState reverse-maps a Todoist task id to its full cell state. Returns
// pgx.ErrNoRows if the id is not one we created — the auto-poster uses that
// as the "not our task, skip" signal. Used in place of the older LookupTask.
func (s *Store) GetTaskState(ctx context.Context, todoistID string) (*TaskState, error) {
	var (
		st        TaskState
		platform  string
		cadence   string
		postedAt  *time.Time
		postedURL *string
	)
	st.TodoistID = todoistID
	err := s.pool.QueryRow(ctx,
		`SELECT t.canonical_url, s.blog_id, t.platform, t.cadence, t.posted_at, t.posted_url
		 FROM blog_post_tasks t
		 JOIN blog_posts_scheduled s ON s.canonical_url = t.canonical_url
		 WHERE t.todoist_id = $1`,
		todoistID,
	).Scan(&st.CanonicalURL, &st.BlogID, &platform, &cadence, &postedAt, &postedURL)
	if err != nil {
		return nil, err
	}
	st.Platform = Platform(platform)
	st.Cadence = Cadence(cadence)
	st.PostedAt = postedAt
	if postedURL != nil {
		st.PostedURL = *postedURL
	}
	return &st, nil
}

// MarkPosted records a successful publish: sets posted_at=now() and posted_url
// on the matching blog_post_tasks row. Idempotent: if posted_at is already
// set, the row is re-written with a newer timestamp (rare — only happens
// when CompleteTask fails and the orchestrator retries; the actual publish
// is deduped at the Mastodon side via Idempotency-Key).
func (s *Store) MarkPosted(ctx context.Context, todoistID, postedURL string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE blog_post_tasks
		 SET posted_at = now(), posted_url = $2
		 WHERE todoist_id = $1`,
		todoistID, postedURL,
	)
	if err != nil {
		return fmt.Errorf("mark_posted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark_posted: no row for todoist_id %s", todoistID)
	}
	return nil
}
