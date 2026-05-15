package blogmarketing

import (
	"context"
	"errors"
	"fmt"

	"darek/exechistory"
	"darek/tools/blogfeed"
	"darek/tools/todoist"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// RegenerateLabel is the Todoist label users add to a single task when they
// want darek to re-draft that one cell. Constant rather than configurable
// because every blog uses the same vocabulary in Todoist and a per-blog
// override would just be a footgun.
const RegenerateLabel = "regenerate"

// RegenerateStore is the subset of *Store that Regenerate uses. Defined
// here so unit tests can supply an in-memory fake. Mirrors PublishStore /
// TaskGetter — one interface per orchestrator, sized to its surface.
type RegenerateStore interface {
	GetTaskState(ctx context.Context, todoistID string) (*TaskState, error)
	GetEntry(ctx context.Context, canonicalURL string) (*blogfeed.Entry, error)
}

// RegenerateTodoistAPI is the Todoist surface used by Regenerate. CompleteTask
// is intentionally absent — re-drafting does NOT close the task; the user
// still needs to action / approve / let the auto-poster handle it.
type RegenerateTodoistAPI interface {
	ListTasks(ctx context.Context, f todoist.ListFilter) ([]todoist.Task, error)
	UpdateTask(ctx context.Context, id string, req todoist.UpdateRequest) (*todoist.Task, error)
}

// RegenerateAccounts maps blog_id → platform → handle. Drafter takes a single
// per-platform handle map per call; this two-level shape lets the regenerator
// pick the right handles for each task's blog without rebuilding the map.
type RegenerateAccounts map[string]map[Platform]string

// RegenerateResult aggregates one regenerate run.
type RegenerateResult struct {
	Regenerated int     // tasks whose content was rewritten + label removed this run
	Skipped     int     // tasks not ours (foreign label users) — silent
	Errors      []error // per-task; siblings continue
}

// Regenerate scans Todoist for tasks carrying the RegenerateLabel, looks up
// each through our store, asks the drafter for a fresh draft of just that
// one (platform, cadence) cell using the persisted entry meta, replaces the
// task content, and removes the label so the cycle is complete from the
// user's POV.
//
// Per-task flow:
//  1. GetTaskState — if not ours (pgx.ErrNoRows), skip silently (someone else
//     uses the same label name in Todoist).
//  2. GetEntry — entry meta required; ErrEntryMetaMissing surfaces clearly
//     so the user can re-schedule or hand-edit the affected post.
//  3. drafter.Draft(entry, accounts) — same call as Sync; we throw away the
//     8 cells we didn't want. (Single-cell drafting is a future optimization
//     if cost matters; today the wasted tokens are a few cents per re-roll.)
//  4. UpdateTask(content: newDraft, labels: existing minus RegenerateLabel).
//     Both fields go in one PATCH so the user sees the new content land and
//     the label disappear atomically.
//
// Returns an aggregate; per-task errors do NOT abort siblings. Hard errors
// from the ListTasks call propagate.
func Regenerate(ctx context.Context, store RegenerateStore, td RegenerateTodoistAPI, drafter Drafter, accounts RegenerateAccounts) (*RegenerateResult, error) {
	ctx, span := tracer.Start(ctx, "blogmarketing.regenerate")
	exechistory.MarkExecution(span, "blog-marketing-regenerate")
	defer span.End()

	res := &RegenerateResult{}

	tasks, err := td.ListTasks(ctx, todoist.ListFilter{Label: RegenerateLabel})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return res, fmt.Errorf("list tasks (label %s): %w", RegenerateLabel, err)
	}

	for _, t := range tasks {
		state, err := store.GetTaskState(ctx, t.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			res.Skipped++
			continue
		}
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: get_task_state: %w", t.ID, err))
			continue
		}

		entry, err := store.GetEntry(ctx, state.CanonicalURL)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: get_entry (blog=%s): %w", t.ID, state.BlogID, err))
			continue
		}

		drafts, err := drafter.Draft(ctx, *entry, accounts[state.BlogID])
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: draft: %w", t.ID, err))
			continue
		}
		newContent, ok := drafts[state.Platform][state.Cadence]
		if !ok || newContent == "" {
			res.Errors = append(res.Errors, fmt.Errorf("%s: drafter returned empty cell for %s/%s",
				t.ID, state.Platform, state.Cadence))
			continue
		}

		newLabels := labelsExcluding(t.Labels, RegenerateLabel)
		req := todoist.UpdateRequest{
			Content: &newContent,
			Labels:  newLabels,
		}
		if _, err := td.UpdateTask(ctx, t.ID, req); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: update_task: %w", t.ID, err))
			continue
		}
		res.Regenerated++
	}

	span.SetAttributes(
		attribute.Int("regenerated", res.Regenerated),
		attribute.Int("skipped", res.Skipped),
		attribute.Int("errors", len(res.Errors)),
	)
	return res, nil
}

// labelsExcluding returns a copy of labels with `drop` removed. Preserves
// order of remaining labels. Used to strip the regenerate label after a
// successful re-draft while keeping the platform/cadence labels intact.
func labelsExcluding(labels []string, drop string) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l == drop {
			continue
		}
		out = append(out, l)
	}
	return out
}
