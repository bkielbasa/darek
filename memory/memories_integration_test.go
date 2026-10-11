//go:build integration

package memory

import (
	"context"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func newMemStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	return NewStore(db.Wrap(pool)), context.Background()
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestSaveMemories_InsertAndDedupe(t *testing.T) {
	s, ctx := newMemStore(t)
	due := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)

	n, err := s.SaveMemories(ctx, []Candidate{{
		Kind: "agreement", Summary: "Dinner with Marek on Sunday",
		People: []string{"Marek"}, DueAt: ptrTime(due), DedupeKey: "Dinner Marek Sunday",
	}}, "Family", []string{"m1"})
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// Same key (different casing) → update, not duplicate.
	n, err = s.SaveMemories(ctx, []Candidate{{
		Kind: "agreement", Summary: "Dinner with Marek this Sunday",
		People: []string{"Marek"}, DedupeKey: "dinner-marek-sunday",
	}}, "Friends", []string{"m2"})
	require.NoError(t, err)
	require.Equal(t, 1, n)

	items, err := s.ActiveItems(ctx, 14, 7, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "Dinner with Marek this Sunday", items[0].Summary)
	require.ElementsMatch(t, []string{"Family", "Friends"}, items[0].Groups)
	require.ElementsMatch(t, []string{"m1", "m2"}, items[0].SourceRefs)
	require.NotNil(t, items[0].DueAt)
}

func TestSaveMemories_PreservesStatusAndDueAt(t *testing.T) {
	s, ctx := newMemStore(t)
	due := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)

	n, err := s.SaveMemories(ctx, []Candidate{{
		Kind: "agreement", Summary: "Send the plan", DedupeKey: "send-plan", DueAt: ptrTime(due),
	}}, "Work", []string{"w1"})
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := s.ListMemories(ctx, "Send the plan", "", "", 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NoError(t, s.SetMemoryStatus(ctx, got[0].ID, "done"))

	// Re-emit with a null due date: status must stay "done", due_at must survive.
	n, err = s.SaveMemories(ctx, []Candidate{{
		Kind: "agreement", Summary: "Send the plan", DedupeKey: "send-plan",
	}}, "Work", []string{"w2"})
	require.NoError(t, err)
	require.Equal(t, 1, n)

	items, err := s.ActiveItems(ctx, 14, 7, 10)
	require.NoError(t, err)
	require.Empty(t, items, "done memory must not appear as active")

	all, err := s.ListMemories(ctx, "", "agreement", "", 10)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "done", all[0].Status)
	require.NotNil(t, all[0].DueAt)
}

func TestActiveItems_FiltersAndOrders(t *testing.T) {
	s, ctx := newMemStore(t)
	now := time.Now().UTC()

	_, err := s.SaveMemories(ctx, []Candidate{
		{Kind: "agreement", Summary: "Overdue thing", DedupeKey: "overdue", DueAt: ptrTime(now.Add(-48 * time.Hour))},
		{Kind: "event", Summary: "Soon thing", DedupeKey: "soon", DueAt: ptrTime(now.Add(48 * time.Hour))},
		{Kind: "event", Summary: "Far thing", DedupeKey: "far", DueAt: ptrTime(now.Add(60 * 24 * time.Hour))},
		{Kind: "agreement", Summary: "Undated recent", DedupeKey: "undated-recent"},
		{Kind: "person_fact", Summary: "Anna likes tea", DedupeKey: "anna-tea"},
	}, "G", []string{"x"})
	require.NoError(t, err)

	items, err := s.ActiveItems(ctx, 14, 7, 10)
	require.NoError(t, err)
	keys := []string{}
	for _, m := range items {
		keys = append(keys, m.DedupeKey)
	}
	require.Equal(t, []string{"overdue", "soon", "undated-recent"}, keys, "ordered by due_at ASC NULLS LAST; far + person_fact excluded")
}

func TestPersonFacts_Window(t *testing.T) {
	s, ctx := newMemStore(t)
	_, err := s.SaveMemories(ctx, []Candidate{{
		Kind: "person_fact", Summary: "Kuba plays football", People: []string{"Anna"}, DedupeKey: "kuba-football",
	}}, "Family", []string{"p1"})
	require.NoError(t, err)

	people, err := s.PersonFacts(ctx, 3, 8)
	require.NoError(t, err)
	require.Len(t, people, 1)
	require.Equal(t, "kuba-football", people[0].DedupeKey)

	// A stale person fact (older than the window) is excluded.
	_, err = s.pool.Exec(ctx, `UPDATE memories SET last_seen_at = now() - interval '10 days' WHERE dedupe_key = 'kuba-football'`)
	require.NoError(t, err)
	people, err = s.PersonFacts(ctx, 3, 8)
	require.NoError(t, err)
	require.Empty(t, people)
}

func TestListMemories_Filters(t *testing.T) {
	s, ctx := newMemStore(t)
	_, err := s.SaveMemories(ctx, []Candidate{
		{Kind: "agreement", Summary: "Berlin trip planning", DedupeKey: "berlin"},
		{Kind: "person_fact", Summary: "Marek fears heights", DedupeKey: "marek-heights"},
	}, "G", []string{"x"})
	require.NoError(t, err)

	byQuery, err := s.ListMemories(ctx, "Berlin", "", "", 5)
	require.NoError(t, err)
	require.Len(t, byQuery, 1)
	require.Equal(t, "berlin", byQuery[0].DedupeKey)

	byKind, err := s.ListMemories(ctx, "", "person_fact", "", 5)
	require.NoError(t, err)
	require.Len(t, byKind, 1)
	require.Equal(t, "marek-heights", byKind[0].DedupeKey)
}
