package blogmarketing

import (
	"context"
	"errors"
	"testing"
	"time"

	"darek/tools/blogfeed"
	"darek/tools/todoist"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type fakeRegenStore struct {
	states  map[string]*TaskState
	entries map[string]*blogfeed.Entry
	entryErr error
}

func (s *fakeRegenStore) GetTaskState(_ context.Context, id string) (*TaskState, error) {
	st, ok := s.states[id]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return st, nil
}

func (s *fakeRegenStore) GetEntry(_ context.Context, canonical string) (*blogfeed.Entry, error) {
	if s.entryErr != nil {
		return nil, s.entryErr
	}
	e, ok := s.entries[canonical]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return e, nil
}

type fakeRegenTodoist struct {
	tasks      []todoist.Task
	listErr    error
	updateErr  error
	updates    map[string]todoist.UpdateRequest
}

func (t *fakeRegenTodoist) ListTasks(_ context.Context, f todoist.ListFilter) ([]todoist.Task, error) {
	if t.listErr != nil {
		return nil, t.listErr
	}
	// Sanity: the regenerator MUST scope to the regenerate label, otherwise
	// it would rewrite every open task.
	if f.Label != RegenerateLabel {
		return nil, errors.New("expected label filter " + RegenerateLabel + ", got " + f.Label)
	}
	return t.tasks, nil
}

func (t *fakeRegenTodoist) UpdateTask(_ context.Context, id string, req todoist.UpdateRequest) (*todoist.Task, error) {
	if t.updateErr != nil {
		return nil, t.updateErr
	}
	if t.updates == nil {
		t.updates = map[string]todoist.UpdateRequest{}
	}
	t.updates[id] = req
	return &todoist.Task{ID: id}, nil
}

type fakeRegenDrafter struct {
	gotEntry    *blogfeed.Entry
	gotAccounts map[Platform]string
	cell        string
	err         error
}

func (d *fakeRegenDrafter) Draft(_ context.Context, e blogfeed.Entry, accounts map[Platform]string) (Drafts, error) {
	d.gotEntry = &e
	d.gotAccounts = accounts
	if d.err != nil {
		return nil, d.err
	}
	out := Drafts{}
	for _, p := range AllPlatforms {
		out[p] = map[Cadence]string{}
		for _, c := range AllCadences {
			out[p][c] = d.cell
		}
	}
	return out, nil
}

func TestRegenerate_HappyPath_RewritesContent_RemovesLabel(t *testing.T) {
	store := &fakeRegenStore{
		states: map[string]*TaskState{
			"task-1": {
				CanonicalURL: "https://blog/p", BlogID: "tech-blog",
				Platform: PlatformX, Cadence: CadenceLaunch, TodoistID: "task-1",
			},
		},
		entries: map[string]*blogfeed.Entry{
			"https://blog/p": {
				CanonicalURL: "https://blog/p", URL: "https://blog/p?utm=rss",
				Title: "Big Post", Summary: "world-changing", PublishedAt: time.Now(),
			},
		},
	}
	td := &fakeRegenTodoist{
		tasks: []todoist.Task{
			{ID: "task-1", Content: "old draft", Labels: []string{"x", "launch", "regenerate"}},
		},
	}
	drafter := &fakeRegenDrafter{cell: "fresh draft for X / launch"}
	accounts := RegenerateAccounts{
		"tech-blog": {PlatformX: "@bk_tech"},
	}

	res, err := Regenerate(context.Background(), store, td, drafter, accounts)
	require.NoError(t, err)
	require.Equal(t, 1, res.Regenerated)
	require.Empty(t, res.Errors)

	got := td.updates["task-1"]
	require.NotNil(t, got.Content)
	require.Equal(t, "fresh draft for X / launch", *got.Content)
	require.Equal(t, []string{"x", "launch"}, got.Labels, "regenerate label must be stripped, others preserved")

	// Drafter received the persisted entry meta (not refetched from any feed),
	// plus the per-blog accounts for the requesting blog only.
	require.NotNil(t, drafter.gotEntry)
	require.Equal(t, "Big Post", drafter.gotEntry.Title)
	require.Equal(t, "@bk_tech", drafter.gotAccounts[PlatformX])
}

func TestRegenerate_TaskNotOurs_SkippedSilently(t *testing.T) {
	store := &fakeRegenStore{states: map[string]*TaskState{}}
	td := &fakeRegenTodoist{
		tasks: []todoist.Task{
			{ID: "someone-elses", Labels: []string{"regenerate"}},
		},
	}
	res, err := Regenerate(context.Background(), store, td, &fakeRegenDrafter{cell: "x"}, RegenerateAccounts{})
	require.NoError(t, err)
	require.Equal(t, 0, res.Regenerated)
	require.Equal(t, 1, res.Skipped)
	require.Empty(t, td.updates, "foreign task must not be updated")
}

func TestRegenerate_EntryMetaMissing_RecordedAsError(t *testing.T) {
	store := &fakeRegenStore{
		states: map[string]*TaskState{
			"task-1": {CanonicalURL: "https://blog/p", BlogID: "tech-blog", TodoistID: "task-1"},
		},
		entryErr: ErrEntryMetaMissing,
	}
	td := &fakeRegenTodoist{
		tasks: []todoist.Task{{ID: "task-1", Labels: []string{"regenerate"}}},
	}
	res, err := Regenerate(context.Background(), store, td, &fakeRegenDrafter{cell: "x"}, RegenerateAccounts{})
	require.NoError(t, err)
	require.Len(t, res.Errors, 1)
	require.ErrorIs(t, res.Errors[0], ErrEntryMetaMissing)
	require.Empty(t, td.updates, "label stays — user can hand-edit and remove it themselves")
}

func TestRegenerate_DrafterError_RecordedSiblingsContinue(t *testing.T) {
	store := &fakeRegenStore{
		states: map[string]*TaskState{
			"bad":  {CanonicalURL: "u-bad", BlogID: "b", Platform: PlatformX, Cadence: CadenceLaunch, TodoistID: "bad"},
			"good": {CanonicalURL: "u-good", BlogID: "b", Platform: PlatformX, Cadence: CadenceLaunch, TodoistID: "good"},
		},
		entries: map[string]*blogfeed.Entry{
			"u-bad":  {CanonicalURL: "u-bad", Title: "bad"},
			"u-good": {CanonicalURL: "u-good", Title: "good"},
		},
	}
	td := &fakeRegenTodoist{
		tasks: []todoist.Task{
			{ID: "bad", Labels: []string{"regenerate"}},
			{ID: "good", Labels: []string{"regenerate"}},
		},
	}
	// Drafter fails for the first call, succeeds afterwards.
	calls := 0
	drafter := drafterFn(func(_ context.Context, e blogfeed.Entry, _ map[Platform]string) (Drafts, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("llm down")
		}
		out := Drafts{}
		for _, p := range AllPlatforms {
			out[p] = map[Cadence]string{}
			for _, c := range AllCadences {
				out[p][c] = e.Title + "-redraft"
			}
		}
		return out, nil
	})

	res, err := Regenerate(context.Background(), store, td, drafter, RegenerateAccounts{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Regenerated, "good must still regenerate")
	require.Len(t, res.Errors, 1)
}

func TestRegenerate_UpdateTaskError_Recorded(t *testing.T) {
	store := &fakeRegenStore{
		states: map[string]*TaskState{
			"task-1": {CanonicalURL: "u", BlogID: "b", Platform: PlatformX, Cadence: CadenceLaunch, TodoistID: "task-1"},
		},
		entries: map[string]*blogfeed.Entry{
			"u": {CanonicalURL: "u", Title: "t"},
		},
	}
	td := &fakeRegenTodoist{
		tasks:     []todoist.Task{{ID: "task-1", Labels: []string{"regenerate"}}},
		updateErr: errors.New("todoist 500"),
	}
	res, err := Regenerate(context.Background(), store, td, &fakeRegenDrafter{cell: "x"}, RegenerateAccounts{})
	require.NoError(t, err)
	require.Equal(t, 0, res.Regenerated)
	require.Len(t, res.Errors, 1)
}

func TestRegenerate_ListTasksError_HardReturn(t *testing.T) {
	store := &fakeRegenStore{}
	td := &fakeRegenTodoist{listErr: errors.New("dns")}
	_, err := Regenerate(context.Background(), store, td, &fakeRegenDrafter{cell: "x"}, RegenerateAccounts{})
	require.Error(t, err)
}

func TestLabelsExcluding(t *testing.T) {
	require.Equal(t, []string{"a", "c"}, labelsExcluding([]string{"a", "b", "c"}, "b"))
	require.Equal(t, []string{"a", "c"}, labelsExcluding([]string{"a", "b", "c", "b"}, "b"), "removes ALL occurrences")
	require.Equal(t, []string{}, labelsExcluding([]string{}, "x"))
	require.Equal(t, []string{"x"}, labelsExcluding([]string{"x"}, "y"), "no-op when target absent")
}

// drafterFn is a function-adapter for the Drafter interface, mirroring
// publish_test.go's publisherFn. Lets a test supply per-call behaviour
// without writing a dedicated fake.
type drafterFn func(ctx context.Context, e blogfeed.Entry, accounts map[Platform]string) (Drafts, error)

func (f drafterFn) Draft(ctx context.Context, e blogfeed.Entry, accounts map[Platform]string) (Drafts, error) {
	return f(ctx, e, accounts)
}

// Compile-time check: *Store satisfies RegenerateStore.
var _ RegenerateStore = (*Store)(nil)
