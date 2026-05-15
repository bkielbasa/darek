package blogmarketing

import (
	"context"
	"errors"
	"testing"
	"time"

	"darek/tools/todoist"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type fakePubStore struct {
	states map[string]*TaskState
	posted map[string]string // todoist_id → posted_url
	getErr error
}

func (s *fakePubStore) GetTaskState(_ context.Context, todoistID string) (*TaskState, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	st, ok := s.states[todoistID]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return st, nil
}

func (s *fakePubStore) MarkPosted(_ context.Context, todoistID, postedURL string) error {
	if s.posted == nil {
		s.posted = map[string]string{}
	}
	s.posted[todoistID] = postedURL
	st, ok := s.states[todoistID]
	if ok {
		now := time.Now()
		st.PostedAt = &now
		st.PostedURL = postedURL
	}
	return nil
}

type fakePubTodoist struct {
	tasks         []todoist.Task
	listErr       error
	updateErr     error
	completeErr   error
	updated       map[string]string // id → new description
	completed     []string
	completeFails map[string]bool // ids whose CompleteTask should error
}

func (t *fakePubTodoist) ListTasks(_ context.Context, _ todoist.ListFilter) ([]todoist.Task, error) {
	if t.listErr != nil {
		return nil, t.listErr
	}
	return t.tasks, nil
}

func (t *fakePubTodoist) UpdateTask(_ context.Context, id string, req todoist.UpdateRequest) (*todoist.Task, error) {
	if t.updateErr != nil {
		return nil, t.updateErr
	}
	if t.updated == nil {
		t.updated = map[string]string{}
	}
	if req.Description != nil {
		t.updated[id] = *req.Description
	}
	return &todoist.Task{ID: id}, nil
}

func (t *fakePubTodoist) CompleteTask(_ context.Context, id string) error {
	if t.completeFails[id] {
		return errors.New("complete failed")
	}
	if t.completeErr != nil {
		return t.completeErr
	}
	t.completed = append(t.completed, id)
	return nil
}

type fakePublisher struct {
	wantContent     string
	wantIdempotency string
	returnURL       string
	returnErr       error
	calls           int
}

func (p *fakePublisher) Publish(_ context.Context, content, idemKey string) (string, error) {
	p.calls++
	if p.wantContent != "" && content != p.wantContent {
		return "", errors.New("unexpected content")
	}
	if p.wantIdempotency != "" && idemKey != p.wantIdempotency {
		return "", errors.New("unexpected idempotency key")
	}
	if p.returnErr != nil {
		return "", p.returnErr
	}
	return p.returnURL, nil
}

func dueNow() *todoist.Due {
	return &todoist.Due{Date: time.Now().Add(-1 * time.Minute).Format(time.RFC3339)}
}

func dueFuture() *todoist.Due {
	return &todoist.Due{Date: time.Now().Add(time.Hour).Format(time.RFC3339)}
}

func TestPublish_HappyPath(t *testing.T) {
	store := &fakePubStore{
		states: map[string]*TaskState{
			"task-1": {
				CanonicalURL: "https://blog/p", BlogID: "tech-blog",
				Platform: PlatformMastodon, Cadence: CadenceLaunch, TodoistID: "task-1",
			},
		},
	}
	td := &fakePubTodoist{
		tasks: []todoist.Task{
			{ID: "task-1", Content: "hello world https://blog/p", Description: "https://blog/p", Due: dueNow()},
		},
	}
	pub := &fakePublisher{
		wantContent:     "hello world https://blog/p",
		wantIdempotency: "darek-publish-task-1",
		returnURL:       "https://fosstodon.org/@bk/1",
	}
	pc := NewPublishConfig()
	pc.Register("tech-blog", PlatformMastodon, pub)

	res, err := Publish(context.Background(), store, td, pc, []string{"P"})
	require.NoError(t, err)
	require.Equal(t, 1, res.Published)
	require.Empty(t, res.Errors)
	require.Equal(t, 1, pub.calls)

	// MarkPosted persisted the post URL.
	require.Equal(t, "https://fosstodon.org/@bk/1", store.posted["task-1"])
	// Linkback appended to description.
	require.Contains(t, td.updated["task-1"], "Posted: https://fosstodon.org/@bk/1")
	// Todoist task closed.
	require.Equal(t, []string{"task-1"}, td.completed)
}

func TestPublish_AlreadyPosted_RetriesCompleteOnly(t *testing.T) {
	earlier := time.Now().Add(-time.Hour)
	store := &fakePubStore{
		states: map[string]*TaskState{
			"task-1": {
				BlogID: "tech-blog", Platform: PlatformMastodon, Cadence: CadenceLaunch,
				TodoistID: "task-1", PostedAt: &earlier, PostedURL: "https://fosstodon.org/@bk/old",
			},
		},
	}
	td := &fakePubTodoist{
		tasks: []todoist.Task{
			{ID: "task-1", Content: "x", Due: dueNow()},
		},
	}
	pub := &fakePublisher{returnURL: "should-not-be-called"}
	pc := NewPublishConfig()
	pc.Register("tech-blog", PlatformMastodon, pub)

	res, err := Publish(context.Background(), store, td, pc, []string{"P"})
	require.NoError(t, err)
	require.Equal(t, 0, res.Published, "must not republish a task that already has posted_at")
	require.Equal(t, 1, res.CompletionRetried)
	require.Equal(t, 0, pub.calls, "publisher must not be invoked")
	require.Equal(t, []string{"task-1"}, td.completed)
}

func TestPublish_TaskNotOurs_Skipped(t *testing.T) {
	store := &fakePubStore{states: map[string]*TaskState{}} // empty
	td := &fakePubTodoist{
		tasks: []todoist.Task{
			{ID: "rando", Content: "user's own todo", Due: dueNow()},
		},
	}
	pc := NewPublishConfig()

	res, err := Publish(context.Background(), store, td, pc, []string{"P"})
	require.NoError(t, err)
	require.Equal(t, 0, res.Published)
	require.Equal(t, 0, res.Skipped, "skipped count is for ours-but-not-due / no-publisher; foreign tasks don't count")
	require.Empty(t, res.Errors)
	require.Empty(t, td.completed, "foreign task must NOT be completed")
}

func TestPublish_NoPublisher_Skipped(t *testing.T) {
	store := &fakePubStore{
		states: map[string]*TaskState{
			"task-1": {BlogID: "tech-blog", Platform: PlatformX, TodoistID: "task-1"},
		},
	}
	td := &fakePubTodoist{
		tasks: []todoist.Task{
			{ID: "task-1", Content: "tweet", Due: dueNow()},
		},
	}
	pc := NewPublishConfig() // no X publisher registered

	res, err := Publish(context.Background(), store, td, pc, []string{"P"})
	require.NoError(t, err)
	require.Equal(t, 0, res.Published)
	require.Equal(t, 1, res.Skipped)
	require.Empty(t, td.completed)
}

func TestPublish_NotDue_Skipped(t *testing.T) {
	store := &fakePubStore{
		states: map[string]*TaskState{
			"task-1": {BlogID: "tech-blog", Platform: PlatformMastodon, TodoistID: "task-1"},
		},
	}
	td := &fakePubTodoist{
		tasks: []todoist.Task{
			{ID: "task-1", Content: "future toot", Due: dueFuture()},
		},
	}
	pc := NewPublishConfig()
	pc.Register("tech-blog", PlatformMastodon, &fakePublisher{returnURL: "x"})

	res, err := Publish(context.Background(), store, td, pc, []string{"P"})
	require.NoError(t, err)
	require.Equal(t, 0, res.Published)
	require.Equal(t, 1, res.Skipped)
}

func TestPublish_PublisherError_RecordedSiblingsContinue(t *testing.T) {
	store := &fakePubStore{
		states: map[string]*TaskState{
			"bad":  {BlogID: "b", Platform: PlatformMastodon, TodoistID: "bad"},
			"good": {BlogID: "b", Platform: PlatformMastodon, TodoistID: "good"},
		},
	}
	td := &fakePubTodoist{
		tasks: []todoist.Task{
			{ID: "bad", Content: "fail", Due: dueNow()},
			{ID: "good", Content: "ok", Due: dueNow()},
		},
	}
	calls := 0
	pub := publisherFn(func(_ context.Context, content, _ string) (string, error) {
		calls++
		if content == "fail" {
			return "", errors.New("api down")
		}
		return "https://fosstodon.org/" + content, nil
	})
	pc := NewPublishConfig()
	pc.Register("b", PlatformMastodon, pub)

	res, err := Publish(context.Background(), store, td, pc, []string{"P"})
	require.NoError(t, err)
	require.Equal(t, 1, res.Published, "good must still post")
	require.Len(t, res.Errors, 1, "bad's failure must be in Errors")
	require.Equal(t, 2, calls)
	require.Equal(t, []string{"good"}, td.completed)
}

func TestPublish_CompleteTaskFails_PostedAtStaysSet(t *testing.T) {
	store := &fakePubStore{
		states: map[string]*TaskState{
			"task-1": {BlogID: "b", Platform: PlatformMastodon, TodoistID: "task-1"},
		},
	}
	td := &fakePubTodoist{
		tasks: []todoist.Task{
			{ID: "task-1", Content: "hi", Due: dueNow()},
		},
		completeFails: map[string]bool{"task-1": true},
	}
	pc := NewPublishConfig()
	pc.Register("b", PlatformMastodon, &fakePublisher{returnURL: "https://fosstodon.org/1"})

	res, err := Publish(context.Background(), store, td, pc, []string{"P"})
	require.NoError(t, err)
	require.Equal(t, 0, res.Published, "Published is the successful-complete count; this task didn't reach there")
	require.Len(t, res.Errors, 1)
	require.NotNil(t, store.states["task-1"].PostedAt, "posted_at must be set so next tick doesn't republish")
}

func TestPublish_ListTasksError_HardReturn(t *testing.T) {
	store := &fakePubStore{}
	td := &fakePubTodoist{listErr: errors.New("dns")}
	pc := NewPublishConfig()

	_, err := Publish(context.Background(), store, td, pc, []string{"P"})
	require.Error(t, err)
}

func TestAppendPostedLinkback(t *testing.T) {
	require.Equal(t, "Posted: https://x/1",
		appendPostedLinkback("", "https://x/1"))
	require.Equal(t, "https://blog/p\nPosted: https://x/1",
		appendPostedLinkback("https://blog/p", "https://x/1"))
	// Idempotent: re-adding when already present leaves it alone.
	desc := "https://blog/p\nPosted: https://x/1"
	require.Equal(t, desc, appendPostedLinkback(desc, "https://x/1"))
}

func TestIsDue(t *testing.T) {
	now := time.Now()
	require.True(t, isDue(&todoist.Due{Date: now.Add(-time.Hour).Format(time.RFC3339)}, now))
	require.True(t, isDue(&todoist.Due{Date: now.Format(time.RFC3339)}, now))
	require.False(t, isDue(&todoist.Due{Date: now.Add(time.Hour).Format(time.RFC3339)}, now))
	require.False(t, isDue(nil, now))
	require.False(t, isDue(&todoist.Due{}, now))
	require.False(t, isDue(&todoist.Due{Date: "garbage"}, now))
}

// publisherFn is a function-adapter for the Publisher interface, used by
// tests that need different per-task behaviour without writing a dedicated
// fake type.
type publisherFn func(ctx context.Context, content, idem string) (string, error)

func (f publisherFn) Publish(ctx context.Context, content, idem string) (string, error) {
	return f(ctx, content, idem)
}

// Compile-time check: *Store satisfies PublishStore.
var _ PublishStore = (*Store)(nil)
