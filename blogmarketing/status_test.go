package blogmarketing

import (
	"context"
	"errors"
	"testing"

	"darek/tools/todoist"

	"github.com/stretchr/testify/require"
)

// fakeStore is the in-memory TaskGetter used by these unit tests. The real
// *Store is exercised by the integration test in store_test.go.
type fakeStore struct {
	refs []TaskRef
	err  error
}

func (s *fakeStore) GetTasks(ctx context.Context, _ string) ([]TaskRef, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.refs, nil
}

// fakeStatusTodoist implements just GetTask of TodoistAPI (the other methods
// aren't reached by GetCampaignStatus). It panics if anything else is called,
// so test scope drift is loud.
type fakeStatusTodoist struct {
	tasks map[string]*todoist.Task
	errs  map[string]error
}

func (f *fakeStatusTodoist) GetTask(ctx context.Context, id string) (*todoist.Task, error) {
	if err, ok := f.errs[id]; ok {
		return nil, err
	}
	if t, ok := f.tasks[id]; ok {
		return t, nil
	}
	return nil, todoist.ErrNotFound
}

func (f *fakeStatusTodoist) ResolveProjectID(context.Context, string) (string, error) {
	panic("ResolveProjectID not expected in GetCampaignStatus tests")
}
func (f *fakeStatusTodoist) CreateTask(context.Context, todoist.CreateRequest) (*todoist.Task, error) {
	panic("CreateTask not expected in GetCampaignStatus tests")
}
func (f *fakeStatusTodoist) DeleteTask(context.Context, string) error {
	panic("DeleteTask not expected in GetCampaignStatus tests")
}

func nineRefs() []TaskRef {
	refs := make([]TaskRef, 0, 9)
	for _, p := range AllPlatforms {
		for _, c := range AllCadences {
			refs = append(refs, TaskRef{Platform: p, Cadence: c, TodoistID: "id-" + string(p) + "-" + string(c)})
		}
	}
	return refs
}

func TestGetCampaignStatus_AllOpen(t *testing.T) {
	store := &fakeStore{refs: nineRefs()}
	td := &fakeStatusTodoist{tasks: map[string]*todoist.Task{}}
	for _, r := range store.refs {
		td.tasks[r.TodoistID] = &todoist.Task{
			ID:      r.TodoistID,
			Content: "draft for " + string(r.Platform) + "/" + string(r.Cadence),
			Labels:  []string{string(r.Platform), string(r.Cadence)},
			URL:     "https://todoist.com/showTask?id=" + r.TodoistID,
			Due:     &todoist.Due{Date: "2026-06-01T09:00:00Z"},
		}
	}
	cells, err := GetCampaignStatus(context.Background(), store, td, "https://example.com/p")
	require.NoError(t, err)
	require.Len(t, cells, 9)

	// Order is AllPlatforms × AllCadences.
	require.Equal(t, PlatformX, cells[0].Platform)
	require.Equal(t, CadenceLaunch, cells[0].Cadence)
	require.Equal(t, PlatformLinkedIn, cells[8].Platform)
	require.Equal(t, CadenceResurface3Mo, cells[8].Cadence)

	for _, c := range cells {
		require.Equal(t, StatusOpen, c.Status)
		require.NotEmpty(t, c.Content)
		require.NotEmpty(t, c.TodoistURL)
		require.Equal(t, 2026, c.Due.Year())
	}
}

func TestGetCampaignStatus_MixedStates(t *testing.T) {
	refs := nineRefs()
	store := &fakeStore{refs: refs}
	td := &fakeStatusTodoist{tasks: map[string]*todoist.Task{}}

	// First cell open, second cell done, third cell missing (omit from map);
	// rest open.
	td.tasks[refs[0].TodoistID] = &todoist.Task{ID: refs[0].TodoistID, Content: "open one"}
	td.tasks[refs[1].TodoistID] = &todoist.Task{ID: refs[1].TodoistID, Content: "done one", IsCompleted: true}
	// refs[2] left out → missing
	for _, r := range refs[3:] {
		td.tasks[r.TodoistID] = &todoist.Task{ID: r.TodoistID, Content: "x"}
	}

	cells, err := GetCampaignStatus(context.Background(), store, td, "url")
	require.NoError(t, err)
	require.Len(t, cells, 9)
	require.Equal(t, StatusOpen, cells[0].Status)
	require.Equal(t, StatusDone, cells[1].Status)
	require.Equal(t, StatusMissing, cells[2].Status)
	require.Empty(t, cells[2].Content, "missing cells must not carry stale content")
}

func TestGetCampaignStatus_NonNotFoundErrorAborts(t *testing.T) {
	refs := nineRefs()
	store := &fakeStore{refs: refs}
	boom := errors.New("todoist 500")
	td := &fakeStatusTodoist{
		tasks: map[string]*todoist.Task{},
		errs:  map[string]error{refs[4].TodoistID: boom},
	}
	for _, r := range refs {
		if r.TodoistID == refs[4].TodoistID {
			continue
		}
		td.tasks[r.TodoistID] = &todoist.Task{ID: r.TodoistID}
	}
	_, err := GetCampaignStatus(context.Background(), store, td, "url")
	require.Error(t, err)
	require.ErrorIs(t, err, boom)
}

func TestGetCampaignStatus_StoreError(t *testing.T) {
	want := errors.New("db down")
	store := &fakeStore{err: want}
	td := &fakeStatusTodoist{}
	_, err := GetCampaignStatus(context.Background(), store, td, "url")
	require.ErrorIs(t, err, want)
}

func TestGetCampaignStatus_EmptyRefs(t *testing.T) {
	store := &fakeStore{refs: nil}
	td := &fakeStatusTodoist{}
	cells, err := GetCampaignStatus(context.Background(), store, td, "url")
	require.NoError(t, err)
	require.Empty(t, cells)
}

func TestParseTodoistDue(t *testing.T) {
	require.Equal(t, 2026, parseTodoistDue("2026-06-01T09:00:00Z").Year())
	require.Equal(t, 2026, parseTodoistDue("2026-06-01").Year())
	require.True(t, parseTodoistDue("not a date").IsZero())
	require.True(t, parseTodoistDue("").IsZero())
}

// Compile-time check: *Store satisfies TaskGetter.
var _ TaskGetter = (*Store)(nil)
