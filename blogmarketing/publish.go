package blogmarketing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"darek/exechistory"
	"darek/obs"
	"darek/tools/mastodon"
	"darek/tools/todoist"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Publisher posts content to a single (blog, platform) social account. Each
// platform (Mastodon today; X / LinkedIn later) implements this. The
// orchestrator picks one by looking up the (blog_id, platform) of the task
// it's about to publish.
type Publisher interface {
	// Publish sends content to the configured account. idempotencyKey, if
	// honored by the platform, dedups retries so a CompleteTask failure
	// after a successful publish doesn't double-post on the next tick.
	Publish(ctx context.Context, content, idempotencyKey string) (postedURL string, err error)
}

// MastodonPublisher adapts *mastodon.Client to the Publisher interface. One
// instance per (blog, mastodon-account) combination.
type MastodonPublisher struct {
	client *mastodon.Client
}

func NewMastodonPublisher(c *mastodon.Client) *MastodonPublisher {
	return &MastodonPublisher{client: c}
}

func (p *MastodonPublisher) Publish(ctx context.Context, content, idempotencyKey string) (string, error) {
	status, err := p.client.Toot(ctx, content, idempotencyKey)
	if err != nil {
		return "", err
	}
	return status.URL, nil
}

// PublishConfig maps blog_id → platform → Publisher. A nil entry means
// "this blog has no credentials for that platform yet" — tasks for that
// (blog, platform) pair are silently skipped, not failed (because the
// drafter may still have populated the LLM prompt with a handle for
// human-eyes use even when no auto-poster is configured).
type PublishConfig struct {
	byBlog map[string]map[Platform]Publisher
}

func NewPublishConfig() *PublishConfig {
	return &PublishConfig{byBlog: map[string]map[Platform]Publisher{}}
}

// Register adds (or replaces) a Publisher for one (blog_id, platform) pair.
// Platform is inferred from whichever Publisher type the caller passes; the
// caller is responsible for matching platform-to-impl correctly.
func (pc *PublishConfig) Register(blogID string, platform Platform, pub Publisher) {
	if _, ok := pc.byBlog[blogID]; !ok {
		pc.byBlog[blogID] = map[Platform]Publisher{}
	}
	pc.byBlog[blogID][platform] = pub
}

// get returns the Publisher for (blogID, platform), or nil if none registered.
func (pc *PublishConfig) get(blogID string, platform Platform) Publisher {
	if pc == nil {
		return nil
	}
	if perPlat, ok := pc.byBlog[blogID]; ok {
		return perPlat[platform]
	}
	return nil
}

// PublishTodoistAPI is the subset of *todoist.Client that Publish uses.
// Separate from TodoistAPI (used by Sync) so the per-feature fakes in tests
// stay focused on the surface their feature actually exercises.
type PublishTodoistAPI interface {
	ListTasks(ctx context.Context, f todoist.ListFilter) ([]todoist.Task, error)
	UpdateTask(ctx context.Context, id string, req todoist.UpdateRequest) (*todoist.Task, error)
	CompleteTask(ctx context.Context, id string) error
}

// PublishStore is the subset of *Store that Publish uses. Interface so unit
// tests can supply an in-memory fake without spinning up Postgres.
type PublishStore interface {
	GetTaskState(ctx context.Context, todoistID string) (*TaskState, error)
	MarkPosted(ctx context.Context, todoistID, postedURL string) error
}

// PublishResult aggregates one Publish run.
type PublishResult struct {
	Published        int     // posts that landed on a platform this run
	CompletionRetried int    // already-posted tasks where we just retried CompleteTask
	Skipped          int     // tasks not ours OR no publisher OR not due yet
	Errors           []error // per-task; siblings continue
}

// Publish scans Todoist tasks across the given projects, finds those that
// are open and due, and publishes them via the registered Publishers.
//
// For each candidate task:
//  1. GetTaskState — if not ours (ErrNoRows), skip silently.
//  2. If PostedAt != nil, publish already happened (last CompleteTask failed) —
//     just retry CompleteTask, don't re-publish.
//  3. Look up Publisher for (blog, platform); nil → skip with reason recorded.
//  4. Build idempotency key from the Todoist id — Mastodon dedups retries.
//  5. publisher.Publish → URL.
//  6. store.MarkPosted(todoist_id, URL).
//  7. UpdateTask description to include "Posted: <URL>" so the user sees the
//     linkback inside Todoist.
//  8. CompleteTask — best-effort; failure leaves posted_at set so next tick
//     re-enters at step 2.
//
// Returns aggregated counts. Hard errors from the upstream ListTasks call
// (network, auth) cause an early hard return; per-task errors are recorded
// in Errors and siblings continue.
func Publish(ctx context.Context, store PublishStore, td PublishTodoistAPI, pc *PublishConfig, projectIDs []string) (*PublishResult, error) {
	ctx, span := tracer.Start(ctx, "blogmarketing.publish")
	exechistory.MarkExecution(span, "blog-marketing-publish")
	defer span.End()

	start := time.Now()
	res := &PublishResult{}
	outcomeStr := "error"
	defer func() {
		recordHist(ctx, start, outcomeStr, func(m *obs.Metrics) metric.Float64Histogram {
			return m.BlogMarketingPublishDuration
		})
	}()
	now := time.Now()

	for _, pid := range projectIDs {
		tasks, err := td.ListTasks(ctx, todoist.ListFilter{ProjectID: pid})
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return res, fmt.Errorf("list tasks (project %s): %w", pid, err)
		}
		for _, t := range tasks {
			if t.IsCompleted {
				continue // ListTasks already filters open-only, defensive guard
			}
			state, err := store.GetTaskState(ctx, t.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // not one of ours
			}
			if err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: get_task_state: %w", t.ID, err))
				continue
			}

			if state.PostedAt != nil {
				// We posted already; last tick's CompleteTask must have failed.
				// Retry CompleteTask without re-publishing.
				if err := td.CompleteTask(ctx, t.ID); err != nil {
					res.Errors = append(res.Errors, fmt.Errorf("%s: retry complete: %w", t.ID, err))
					continue
				}
				res.CompletionRetried++
				continue
			}

			if !isDue(t.Due, now) {
				res.Skipped++
				continue
			}

			pub := pc.get(state.BlogID, state.Platform)
			if pub == nil {
				// Drafted but no auto-poster credentials wired for this
				// (blog, platform). Leave for the user to action manually.
				res.Skipped++
				continue
			}

			idem := "darek-publish-" + t.ID
			postedURL, err := pub.Publish(ctx, t.Content, idem)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: publish (blog=%s platform=%s): %w",
					t.ID, state.BlogID, state.Platform, err))
				continue
			}

			// Mark posted in DB BEFORE CompleteTask, so a crash after this
			// point keeps us out of double-publishing on retry.
			if err := store.MarkPosted(ctx, t.ID, postedURL); err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: mark_posted: %w", t.ID, err))
				continue
			}

			// Linkback in the Todoist task description so the user can see
			// where the post landed without leaving Todoist.
			newDesc := appendPostedLinkback(t.Description, postedURL)
			if newDesc != t.Description {
				if _, uerr := td.UpdateTask(ctx, t.ID, todoist.UpdateRequest{Description: &newDesc}); uerr != nil {
					// Cosmetic; do not block CompleteTask on this.
					span.AddEvent("linkback update failed", trace.WithAttributes(
						attribute.String("todoist_id", t.ID),
						attribute.String("error", uerr.Error()),
					))
				}
			}

			if err := td.CompleteTask(ctx, t.ID); err != nil {
				// posted_at is set; next tick will see PostedAt != nil and retry CompleteTask only.
				res.Errors = append(res.Errors, fmt.Errorf("%s: complete: %w", t.ID, err))
				continue
			}
			res.Published++
		}
	}

	span.SetAttributes(
		attribute.Int("published", res.Published),
		attribute.Int("completion_retried", res.CompletionRetried),
		attribute.Int("skipped", res.Skipped),
		attribute.Int("errors", len(res.Errors)),
	)
	outcomeStr = publishOutcome(res)
	return res, nil
}

// isDue is true when the Todoist Due payload's date/datetime is at or before
// `now`. Tasks with no Due (nil or empty Date) are NOT considered due — only
// explicitly scheduled cells should auto-publish.
func isDue(d *todoist.Due, now time.Time) bool {
	if d == nil || d.Date == "" {
		return false
	}
	parsed := parseTodoistDue(d.Date) // reuses status.go's tolerant parser
	if parsed.IsZero() {
		return false
	}
	return !parsed.After(now)
}

// appendPostedLinkback adds "Posted: <url>" on its own line if not already
// present. Idempotent: a duplicate run leaves the description unchanged.
func appendPostedLinkback(description, postedURL string) string {
	if postedURL == "" {
		return description
	}
	if strings.Contains(description, postedURL) {
		return description
	}
	if description == "" {
		return "Posted: " + postedURL
	}
	return description + "\nPosted: " + postedURL
}

