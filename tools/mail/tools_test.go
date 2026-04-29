//go:build integration

package mail

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

type accountResolver map[string]MailAccount

func (a accountResolver) ByNickname(n string) (MailAccount, bool) { x, ok := a[n]; return x, ok }

func TestSearchTool(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	aid, _ := s.EnsureAccount(ctx, AccountSpec{Nickname: "p", Email: "m@x", IMAPHost: "h", IMAPPort: 993, IMAPTLS: true, Username: "u", SecretRef: "env:S"})
	fid, _, _, _ := s.EnsureFolder(ctx, aid, "INBOX")
	_, _ = s.InsertEnvelope(ctx, aid, fid, Envelope{UID: 1, Subject: "Berlin trip", From: "anna@x.com", Date: time.Now(), Snippet: "tickets"})

	out, err := SearchTool{Store: s}.Execute(ctx, json.RawMessage(`{"query":"berlin"}`))
	require.NoError(t, err)
	require.Contains(t, out, "Berlin trip")
}

func TestGetBodyTool(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	aid, _ := s.EnsureAccount(ctx, AccountSpec{Nickname: "p", Email: "m@x", IMAPHost: "h", IMAPPort: 993, IMAPTLS: true, Username: "u", SecretRef: "env:S"})
	fid, _, _, _ := s.EnsureFolder(ctx, aid, "INBOX")
	mid, _ := s.InsertEnvelope(ctx, aid, fid, Envelope{UID: 1, Subject: "x", Date: time.Now()})

	acc := &fakeAccount{nickname: "p", body: "hello body"}
	tool := GetBodyTool{Store: s, Accounts: accountResolver{"p": acc}}
	out, err := tool.Execute(ctx, json.RawMessage(`{"message_id":"`+mid.String()+`"}`))
	require.NoError(t, err)
	require.Equal(t, "hello body", out)
}

func TestGetAttachmentTool_WritesFile(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	aid, _ := s.EnsureAccount(ctx, AccountSpec{Nickname: "p", Email: "m@x", IMAPHost: "h", IMAPPort: 993, IMAPTLS: true, Username: "u", SecretRef: "env:S"})
	fid, _, _, _ := s.EnsureFolder(ctx, aid, "INBOX")
	mid, _ := s.InsertEnvelope(ctx, aid, fid, Envelope{
		UID: 1, Subject: "x", Date: time.Now(),
		Attachments: []AttachmentMeta{{Filename: "doc.pdf", ContentType: "application/pdf", PartID: "1.2"}},
	})

	// Find the attachment id we just created.
	var attID string
	require.NoError(t, pool.QueryRow(ctx, `SELECT id::text FROM mail_attachments_meta WHERE message_id = $1`, mid).Scan(&attID))

	dir := t.TempDir()
	acc := &accountWithAttach{fakeAccount: fakeAccount{nickname: "p"}, payload: "PDF-BYTES"}
	tool := GetAttachmentTool{Store: s, Accounts: accountResolver{"p": acc}, AttachmentsDir: dir}
	path, err := tool.Execute(ctx, json.RawMessage(`{"attachment_id":"`+attID+`"}`))
	require.NoError(t, err)
	require.Contains(t, path, "doc.pdf")
}

type accountWithAttach struct {
	fakeAccount
	payload string
}

func (a *accountWithAttach) FetchAttachment(_ context.Context, _ string, _ uint32, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(a.payload)), nil
}
