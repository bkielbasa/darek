//go:build integration

package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"
	"darek/tools/whatsapp"

	"github.com/stretchr/testify/require"
)

// stubSource implements TranscriptSource with in-memory data.
type stubSource struct {
	groups    []whatsapp.Group
	msgs      map[string][]whatsapp.Message
	marked    []string
	markCalls int
}

func (s *stubSource) OptedInGroups(_ context.Context) ([]whatsapp.Group, error) {
	return s.groups, nil
}
func (s *stubSource) UnextractedMessages(_ context.Context, jid string, _ int) ([]whatsapp.Message, error) {
	return s.msgs[jid], nil
}
func (s *stubSource) MarkMemoriesExtracted(_ context.Context, ids []string) error {
	s.markCalls++
	s.marked = append(s.marked, ids...)
	return nil
}

type stubExtractor struct {
	byGroup    map[string][]Candidate
	errByGroup map[string]error
	calls      []string
}

func (s *stubExtractor) Extract(_ context.Context, group string, _ []whatsapp.Message) ([]Candidate, error) {
	s.calls = append(s.calls, group)
	if err, ok := s.errByGroup[group]; ok {
		return nil, err
	}
	return s.byGroup[group], nil
}

func newOrchStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	return NewStore(db.Wrap(pool)), context.Background()
}

func waMsg(id, group string) whatsapp.Message {
	return whatsapp.Message{ID: id, GroupJID: group, SenderJID: "x", SenderName: "S", Kind: "text", Body: "b", SentAt: time.Now()}
}

func TestBuildMemories_HappyPath(t *testing.T) {
	store, ctx := newOrchStore(t)
	src := &stubSource{
		groups: []whatsapp.Group{{JID: "g1@g.us", Name: "Family"}, {JID: "g2@g.us", Name: "Quiet"}},
		msgs: map[string][]whatsapp.Message{
			"g1@g.us": {waMsg("m1", "g1@g.us"), waMsg("m2", "g1@g.us")},
		},
	}
	ex := &stubExtractor{byGroup: map[string][]Candidate{
		"Family": {{Kind: "agreement", Summary: "Dinner Sunday", DedupeKey: "dinner-sunday"}},
	}}

	st, err := BuildMemories(ctx, src, store, ex, nil)
	require.NoError(t, err)
	require.Equal(t, 2, st.Groups)
	require.Equal(t, 1, st.Extracted)
	require.Equal(t, 1, st.Saved)
	require.Equal(t, 0, st.FailedGroups)
	require.ElementsMatch(t, []string{"m1", "m2"}, src.marked)

	items, err := store.ActiveItems(ctx, 14, 7, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "Family", items[0].Groups[0])
}

func TestBuildMemories_NoCandidatesStillMarks(t *testing.T) {
	store, ctx := newOrchStore(t)
	src := &stubSource{
		groups: []whatsapp.Group{{JID: "g1@g.us", Name: "Chatty"}},
		msgs:   map[string][]whatsapp.Message{"g1@g.us": {waMsg("m1", "g1@g.us")}},
	}
	ex := &stubExtractor{byGroup: map[string][]Candidate{"Chatty": nil}}

	st, err := BuildMemories(ctx, src, store, ex, nil)
	require.NoError(t, err)
	require.Equal(t, 0, st.Saved)
	require.Equal(t, []string{"m1"}, src.marked, "read-but-nothing-durable messages are still marked")
}

func TestBuildMemories_FailedGroupDoesNotAbort(t *testing.T) {
	store, ctx := newOrchStore(t)
	src := &stubSource{
		groups: []whatsapp.Group{{JID: "g1@g.us", Name: "Good"}, {JID: "g2@g.us", Name: "Bad"}},
		msgs: map[string][]whatsapp.Message{
			"g1@g.us": {waMsg("g1m", "g1@g.us")},
			"g2@g.us": {waMsg("g2m", "g2@g.us")},
		},
	}
	ex := &stubExtractor{
		byGroup:    map[string][]Candidate{"Good": {{Kind: "event", Summary: "x", DedupeKey: "x"}}},
		errByGroup: map[string]error{"Bad": errors.New("nope")},
	}

	st, err := BuildMemories(ctx, src, store, ex, nil)
	require.NoError(t, err)
	require.Equal(t, 1, st.FailedGroups)
	require.Equal(t, 1, st.Saved)
	require.Equal(t, []string{"g1m"}, src.marked, "failed group's IDs are not marked (retry)")
}
