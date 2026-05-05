//go:build integration

package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

// stubSummarizer is the SummarizerInterface fake used by BuildSummary tests.
type stubSummarizer struct {
	respByGroup map[string]string
	errByGroup  map[string]error
	calls       []string
}

func (s *stubSummarizer) Summarize(ctx context.Context, groupName string, msgs []Message) (string, error) {
	s.calls = append(s.calls, groupName)
	if err, ok := s.errByGroup[groupName]; ok {
		return "", err
	}
	if r, ok := s.respByGroup[groupName]; ok {
		return r, nil
	}
	return "default summary for " + groupName, nil
}

func newSummaryTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	return NewStore(db.Wrap(raw)), context.Background()
}

func TestBuildSummary_HappyPath(t *testing.T) {
	s, ctx := newSummaryTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "Family"))
	require.NoError(t, s.UpsertGroup(ctx, "g2@g.us", "Work"))
	require.NoError(t, s.SetIngestEnabled(ctx, "g1@g.us", true))
	require.NoError(t, s.SetIngestEnabled(ctx, "g2@g.us", true))

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "a", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "Bart", Kind: "text", Body: "hey", SentAt: now.Add(-2 * time.Hour)}))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "b", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "Asia", Kind: "text", Body: "hi", SentAt: now.Add(-1 * time.Hour)}))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "c", GroupJID: "g2@g.us", SenderJID: "x", SenderName: "Karol", Kind: "text", Body: "report?", SentAt: now}))

	stub := &stubSummarizer{
		respByGroup: map[string]string{
			"Family": "Family chat sample.",
			"Work":   "Work chat sample.",
		},
	}
	sections, ids, err := BuildSummary(ctx, s, stub, nil)
	require.NoError(t, err)
	require.Len(t, sections, 2)
	require.ElementsMatch(t, []string{"Family", "Work"}, []string{sections[0].GroupName, sections[1].GroupName})
	require.ElementsMatch(t, []string{"a", "b", "c"}, ids)
}

func TestBuildSummary_SkipsOptedOutGroup(t *testing.T) {
	s, ctx := newSummaryTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "Tracked"))
	require.NoError(t, s.UpsertGroup(ctx, "g2@g.us", "Untracked"))
	require.NoError(t, s.SetIngestEnabled(ctx, "g1@g.us", true))

	now := time.Now().UTC()
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "a", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "S", Kind: "text", Body: "x", SentAt: now}))

	stub := &stubSummarizer{}
	sections, ids, err := BuildSummary(ctx, s, stub, nil)
	require.NoError(t, err)
	require.Len(t, sections, 1)
	require.Equal(t, "Tracked", sections[0].GroupName)
	require.Equal(t, []string{"a"}, ids)
}

func TestBuildSummary_SkipsGroupWithNoUnsummarized(t *testing.T) {
	s, ctx := newSummaryTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "Quiet"))
	require.NoError(t, s.UpsertGroup(ctx, "g2@g.us", "Active"))
	require.NoError(t, s.SetIngestEnabled(ctx, "g1@g.us", true))
	require.NoError(t, s.SetIngestEnabled(ctx, "g2@g.us", true))

	now := time.Now().UTC()
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "a", GroupJID: "g2@g.us", SenderJID: "x", SenderName: "S", Kind: "text", Body: "x", SentAt: now}))

	stub := &stubSummarizer{}
	sections, _, err := BuildSummary(ctx, s, stub, nil)
	require.NoError(t, err)
	require.Len(t, sections, 1)
	require.Equal(t, "Active", sections[0].GroupName)
}

func TestBuildSummary_FailedGroupExcludedFromIDs(t *testing.T) {
	s, ctx := newSummaryTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "Good"))
	require.NoError(t, s.UpsertGroup(ctx, "g2@g.us", "Bad"))
	require.NoError(t, s.SetIngestEnabled(ctx, "g1@g.us", true))
	require.NoError(t, s.SetIngestEnabled(ctx, "g2@g.us", true))

	now := time.Now().UTC()
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "g1m", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "S", Kind: "text", Body: "x", SentAt: now}))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "g2m", GroupJID: "g2@g.us", SenderJID: "x", SenderName: "S", Kind: "text", Body: "y", SentAt: now}))

	stub := &stubSummarizer{
		errByGroup: map[string]error{"Bad": errors.New("nope")},
	}
	sections, ids, err := BuildSummary(ctx, s, stub, nil)
	require.NoError(t, err, "one bad group must not abort the run")
	require.Len(t, sections, 1)
	require.Equal(t, "Good", sections[0].GroupName)
	require.Equal(t, []string{"g1m"}, ids, "Bad group's message IDs are not in the mark-summarized list")
}

func TestBuildSummary_NoGroupsReturnsEmpty(t *testing.T) {
	s, ctx := newSummaryTestStore(t)

	stub := &stubSummarizer{}
	sections, ids, err := BuildSummary(ctx, s, stub, nil)
	require.NoError(t, err)
	require.Empty(t, sections)
	require.Empty(t, ids)
	require.Empty(t, stub.calls, "summarizer must not be called when no opted-in groups exist")
}
