package blogmarketing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"darek/tools/todoist"

	"golang.org/x/sync/errgroup"
)

// Status is the lifecycle state of a single scheduled Todoist task, as seen
// live from Todoist at the moment GetCampaignStatus runs.
type Status string

const (
	StatusOpen    Status = "open"    // task exists and is not yet completed
	StatusDone    Status = "done"    // task exists and is_completed=true
	StatusMissing Status = "missing" // task no longer exists in Todoist (user deleted)
)

// CellState is the reverse-synced view of one of the 9 cells (platform × cadence)
// for a single blog post. Status drives which other fields are populated:
//   - open / done: Content, Due (best-effort parsed), Labels, TodoistURL are filled.
//   - missing:     only Platform, Cadence, TodoistID are filled.
type CellState struct {
	Platform   Platform
	Cadence    Cadence
	TodoistID  string
	Status     Status
	Content    string
	Due        time.Time
	Labels     []string
	TodoistURL string
}

// TaskGetter is the subset of *Store that GetCampaignStatus needs. Defined
// as an interface so unit tests can supply an in-memory fake instead of a
// real Postgres testcontainer.
type TaskGetter interface {
	GetTasks(ctx context.Context, canonicalURL string) ([]TaskRef, error)
}

// GetCampaignStatus returns the live state of every Todoist task that backs
// the campaign for canonicalURL. The result is ordered by AllPlatforms × AllCadences,
// preserving the canonical UI grid. Cells with no row in blog_post_tasks are
// omitted from the result (that path is only hit for seen-only / unscheduled
// posts; for fully-scheduled posts the slice always has 9 entries).
//
// Per-cell GetTask calls run concurrently. A 404 on any cell maps to
// StatusMissing (the user deleted the task); any other Todoist error aborts
// the whole call and is returned wrapped — a partial render would mislead
// the UI.
func GetCampaignStatus(ctx context.Context, store TaskGetter, td TodoistAPI, canonicalURL string) ([]CellState, error) {
	refs, err := store.GetTasks(ctx, canonicalURL)
	if err != nil {
		return nil, fmt.Errorf("get_campaign_status: %w", err)
	}

	byCell := make(map[Platform]map[Cadence]TaskRef, len(AllPlatforms))
	for _, p := range AllPlatforms {
		byCell[p] = make(map[Cadence]TaskRef, len(AllCadences))
	}
	for _, r := range refs {
		if _, ok := byCell[r.Platform]; !ok {
			continue
		}
		byCell[r.Platform][r.Cadence] = r
	}

	var cells []CellState
	for _, p := range AllPlatforms {
		for _, c := range AllCadences {
			r, ok := byCell[p][c]
			if !ok {
				continue
			}
			cells = append(cells, CellState{Platform: p, Cadence: c, TodoistID: r.TodoistID})
		}
	}

	g, gctx := errgroup.WithContext(ctx)
	for i := range cells {
		g.Go(func() error {
			cell := &cells[i]
			task, terr := td.GetTask(gctx, cell.TodoistID)
			if errors.Is(terr, todoist.ErrNotFound) {
				cell.Status = StatusMissing
				return nil
			}
			if terr != nil {
				return fmt.Errorf("get_campaign_status %s (%s/%s): %w",
					cell.TodoistID, cell.Platform, cell.Cadence, terr)
			}
			if task.IsCompleted {
				cell.Status = StatusDone
			} else {
				cell.Status = StatusOpen
			}
			cell.Content = task.Content
			cell.Labels = task.Labels
			cell.TodoistURL = task.URL
			if task.Due != nil && task.Due.Date != "" {
				cell.Due = parseTodoistDue(task.Due.Date)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return cells, nil
}

// parseTodoistDue accepts the date strings Todoist puts in Due.Date — either
// RFC 3339 (timed due) or YYYY-MM-DD (date-only due). Returns the zero time
// on parse failure; callers treat zero as "no usable due".
func parseTodoistDue(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return time.Time{}
}
