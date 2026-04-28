//go:build integration

package mail

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

type fakeSender struct {
	from string
	rcpt []string
	raw  []byte
	err  error
}

func (f *fakeSender) Send(from string, rcpt []string, raw []byte) error {
	f.from = from
	f.rcpt = rcpt
	f.raw = raw
	return f.err
}

type fakeAppender struct {
	folder string
	flags  []string
	raw    []byte
	err    error
}

func (f *fakeAppender) Append(_ context.Context, folder string, flags []string, raw []byte) error {
	f.folder = folder
	f.flags = flags
	f.raw = raw
	return f.err
}

type fakeSendResolver struct{ deps map[string]SendDeps }

func (f fakeSendResolver) SendDepsFor(n string) (SendDeps, bool) { d, ok := f.deps[n]; return d, ok }

type alwaysYes struct{}

func (alwaysYes) Confirm(_ context.Context, _ Preview) (bool, error) { return true, nil }

type alwaysNo struct{}

func (alwaysNo) Confirm(_ context.Context, _ Preview) (bool, error) { return false, nil }

func TestSendTool_HappyPath_AppendsToSent(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(pool)
	ctx := context.Background()

	smtp := &fakeSender{}
	app := &fakeAppender{}
	resolver := fakeSendResolver{deps: map[string]SendDeps{
		"p": {From: "me@x.com", SMTP: smtp, Appender: app, SentFolder: "Sent"},
	}}
	tool := SendTool{Store: s, Accounts: resolver, Confirm: alwaysYes{}}
	out, err := tool.Execute(ctx, json.RawMessage(`{"account":"p","to":["a@x.com"],"subject":"hi","body":"hello"}`))
	require.NoError(t, err)
	require.Contains(t, out, "sent (message-id ")
	require.Equal(t, "me@x.com", smtp.from)
	require.Equal(t, []string{"a@x.com"}, smtp.rcpt)
	require.Contains(t, string(smtp.raw), "Subject: hi")
	require.Equal(t, "Sent", app.folder)
}

func TestSendTool_DeclinedReturnsCleanly(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(pool)
	ctx := context.Background()

	smtp := &fakeSender{}
	resolver := fakeSendResolver{deps: map[string]SendDeps{
		"p": {From: "me@x.com", SMTP: smtp},
	}}
	tool := SendTool{Store: s, Accounts: resolver, Confirm: alwaysNo{}}
	out, err := tool.Execute(ctx, json.RawMessage(`{"account":"p","to":["a@x.com"],"body":"hi"}`))
	require.NoError(t, err)
	require.Equal(t, "user declined to send", out)
	require.Empty(t, smtp.from)
}

func TestSendTool_ReplyResolvesThreading(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(pool)
	ctx := context.Background()

	aid, _ := s.EnsureAccount(ctx, AccountSpec{Nickname: "p", Email: "m@x", IMAPHost: "h", IMAPPort: 993, IMAPTLS: true, Username: "u", SecretRef: "env:S"})
	fid, _, _, _ := s.EnsureFolder(ctx, aid, "INBOX")
	mid, _ := s.InsertEnvelope(ctx, aid, fid, Envelope{
		UID: 7, MessageID: "orig@host", Subject: "Original",
		References: []string{"first@host"}, Date: time.Now(),
	})

	smtp := &fakeSender{}
	resolver := fakeSendResolver{deps: map[string]SendDeps{
		"p": {From: "me@x.com", SMTP: smtp},
	}}
	tool := SendTool{Store: s, Accounts: resolver, Confirm: alwaysYes{}}
	out, err := tool.Execute(ctx, json.RawMessage(`{"account":"p","to":["a@x.com"],"body":"reply","in_reply_to":"`+mid.String()+`"}`))
	require.NoError(t, err)
	require.Contains(t, out, "sent (message-id ")
	require.Contains(t, string(smtp.raw), "Subject: Re: Original")
	require.Contains(t, string(smtp.raw), "In-Reply-To: <orig@host>")
	require.Contains(t, string(smtp.raw), "References: <first@host> <orig@host>")
}

func TestSendTool_SMTPErrorPropagates(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(pool)
	ctx := context.Background()

	smtp := &fakeSender{err: errors.New("server down")}
	resolver := fakeSendResolver{deps: map[string]SendDeps{
		"p": {From: "me@x.com", SMTP: smtp},
	}}
	tool := SendTool{Store: s, Accounts: resolver, Confirm: alwaysYes{}}
	_, err := tool.Execute(ctx, json.RawMessage(`{"account":"p","to":["a@x.com"],"body":"x"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "smtp send")
}
