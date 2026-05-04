//go:build integration

package whatsapp

import (
	"context"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	return NewStore(raw), context.Background()
}

func TestStore_UpsertGroup_PreservesIngestEnabled(t *testing.T) {
	s, ctx := newTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "Old Name"))
	require.NoError(t, s.SetIngestEnabled(ctx, "g1@g.us", true))

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "New Name"))

	groups, err := s.Groups(ctx)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Equal(t, "New Name", groups[0].Name)
	require.True(t, groups[0].IngestEnabled, "user opt-in must survive metadata refresh")
}

func TestStore_SetIngestEnabled(t *testing.T) {
	s, ctx := newTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "G1"))

	exists, enabled, err := s.IngestEnabled(ctx, "g1@g.us")
	require.NoError(t, err)
	require.True(t, exists)
	require.False(t, enabled)

	require.NoError(t, s.SetIngestEnabled(ctx, "g1@g.us", true))
	exists, enabled, err = s.IngestEnabled(ctx, "g1@g.us")
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, enabled)

	exists, _, err = s.IngestEnabled(ctx, "missing@g.us")
	require.NoError(t, err)
	require.False(t, exists)
}

func TestStore_InsertMessage_Idempotent(t *testing.T) {
	s, ctx := newTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "G1"))

	msg := Message{
		ID:         "M1",
		GroupJID:   "g1@g.us",
		SenderJID:  "1234@s.whatsapp.net",
		SenderName: "Bart",
		Kind:       "text",
		Body:       "hello",
		SentAt:     time.Now().UTC().Truncate(time.Second),
	}
	require.NoError(t, s.InsertMessage(ctx, msg))
	require.NoError(t, s.InsertMessage(ctx, msg))

	groups, err := s.Groups(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, groups[0].MessageCount, "duplicate insert must not double-count")
	require.NotNil(t, groups[0].LastMessageAt)
	require.True(t, groups[0].LastMessageAt.Equal(msg.SentAt))
}

func TestStore_Groups_CountsAndLast(t *testing.T) {
	s, ctx := newTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "G1"))
	require.NoError(t, s.UpsertGroup(ctx, "g2@g.us", "G2"))

	t1 := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	t2 := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)

	require.NoError(t, s.InsertMessage(ctx, Message{ID: "a", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "x", Kind: "text", Body: "1", SentAt: t1}))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "b", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "x", Kind: "text", Body: "2", SentAt: t2}))

	groups, err := s.Groups(ctx)
	require.NoError(t, err)
	require.Len(t, groups, 2)
	byJID := map[string]Group{}
	for _, g := range groups {
		byJID[g.JID] = g
	}
	require.Equal(t, 2, byJID["g1@g.us"].MessageCount)
	require.NotNil(t, byJID["g1@g.us"].LastMessageAt)
	require.True(t, byJID["g1@g.us"].LastMessageAt.Equal(t2), "LastMessageAt is the most recent")
	require.Equal(t, 0, byJID["g2@g.us"].MessageCount)
	require.Nil(t, byJID["g2@g.us"].LastMessageAt)
}

func TestStore_DeleteGroupCascadesMessages(t *testing.T) {
	s, ctx := newTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "G1"))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "m1", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "x", Kind: "text", Body: "x", SentAt: time.Now().UTC()}))

	_, err := s.pool.Exec(ctx, `DELETE FROM whatsapp_groups WHERE jid = $1`, "g1@g.us")
	require.NoError(t, err)

	var count int
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM whatsapp_messages`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}
