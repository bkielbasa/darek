//go:build integration

package mail

import (
	"context"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func TestStore_EnsureAccountAndFolder(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	id, err := s.EnsureAccount(ctx, AccountSpec{
		Nickname: "personal", Email: "me@x.com",
		IMAPHost: "h", IMAPPort: 993, IMAPTLS: true,
		Username: "u", SecretRef: "env:S",
	})
	require.NoError(t, err)
	require.NotEqual(t, "00000000-0000-0000-0000-000000000000", id.String())

	fid, uv, lu, err := s.EnsureFolder(ctx, id, "INBOX")
	require.NoError(t, err)
	require.NotEqual(t, "00000000-0000-0000-0000-000000000000", fid.String())
	require.Equal(t, uint32(0), uv)
	require.Equal(t, uint32(0), lu)
}

func TestStore_InsertEnvelopeAndSearch(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	aid, _ := s.EnsureAccount(ctx, AccountSpec{Nickname: "p", Email: "m@x", IMAPHost: "h", IMAPPort: 993, IMAPTLS: true, Username: "u", SecretRef: "env:S"})
	fid, _, _, _ := s.EnsureFolder(ctx, aid, "INBOX")

	_, err := s.InsertEnvelope(ctx, aid, fid, Envelope{
		UID: 1, MessageID: "<a>", From: "anna@x.com", To: []string{"me@x.com"},
		Subject: "Berlin trip", Date: time.Now(), Snippet: "tickets attached",
		HasAttach: true, Attachments: []AttachmentMeta{{Filename: "tickets.pdf", ContentType: "application/pdf", PartID: "1.2"}},
	})
	require.NoError(t, err)
	res, err := s.Search(ctx, SearchOpts{Query: "berlin"})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, "Berlin trip", res[0].Subject)
	require.True(t, res[0].HasAttach)
}

func TestStore_UpsertOnConflictKeepsRow(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	aid, _ := s.EnsureAccount(ctx, AccountSpec{Nickname: "p", Email: "m@x", IMAPHost: "h", IMAPPort: 993, IMAPTLS: true, Username: "u", SecretRef: "env:S"})
	fid, _, _, _ := s.EnsureFolder(ctx, aid, "INBOX")

	id1, err := s.InsertEnvelope(ctx, aid, fid, Envelope{UID: 7, Subject: "hi", Flags: []string{"\\Seen"}})
	require.NoError(t, err)
	id2, err := s.InsertEnvelope(ctx, aid, fid, Envelope{UID: 7, Subject: "hi-again", Flags: []string{"\\Flagged"}})
	require.NoError(t, err)
	require.Equal(t, id1, id2) // upsert returns the same id; only flags update
}
