//go:build integration

package mail

import (
	"context"
	"io"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

// fakeAccount implements MailAccount without touching IMAP.
type fakeAccount struct {
	nickname string
	envs     map[string][]Envelope
	uvs      map[string]uint32
	body     string
}

func (f *fakeAccount) Nickname() string { return f.nickname }
func (f *fakeAccount) Email() string    { return f.nickname + "@x.com" }
func (f *fakeAccount) SyncFolder(_ context.Context, folder string, sinceUID uint32) ([]Envelope, uint32, error) {
	out := make([]Envelope, 0)
	for _, e := range f.envs[folder] {
		if e.UID > sinceUID {
			out = append(out, e)
		}
	}
	return out, f.uvs[folder], nil
}
func (f *fakeAccount) FetchBody(_ context.Context, _ string, _ uint32) (string, error) {
	return f.body, nil
}
func (f *fakeAccount) FetchAttachment(_ context.Context, _ string, _ uint32, _ string) (io.ReadCloser, error) {
	return nil, nil
}

func TestSync_FirstPass(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	aid, _ := s.EnsureAccount(ctx, AccountSpec{Nickname: "p", Email: "m@x", IMAPHost: "h", IMAPPort: 993, IMAPTLS: true, Username: "u", SecretRef: "env:S"})

	acc := &fakeAccount{
		nickname: "p",
		uvs:      map[string]uint32{"INBOX": 100},
		envs: map[string][]Envelope{
			"INBOX": {
				{UID: 1, Subject: "first", Date: time.Now()},
				{UID: 2, Subject: "second", Date: time.Now()},
			},
		},
	}
	reports, err := Sync(ctx, s, aid, acc, []string{"INBOX"})
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, 2, reports[0].NewMessages)

	res, err := s.Search(ctx, SearchOpts{})
	require.NoError(t, err)
	require.Len(t, res, 2)
}

func TestSync_UIDValidityChange_Resyncs(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	aid, _ := s.EnsureAccount(ctx, AccountSpec{Nickname: "p", Email: "m@x", IMAPHost: "h", IMAPPort: 993, IMAPTLS: true, Username: "u", SecretRef: "env:S"})

	acc := &fakeAccount{nickname: "p", uvs: map[string]uint32{"INBOX": 1}, envs: map[string][]Envelope{
		"INBOX": {{UID: 100, Subject: "old"}},
	}}
	_, err := Sync(ctx, s, aid, acc, []string{"INBOX"})
	require.NoError(t, err)

	acc.uvs["INBOX"] = 2
	acc.envs["INBOX"] = []Envelope{{UID: 1, Subject: "new"}}
	_, err = Sync(ctx, s, aid, acc, []string{"INBOX"})
	require.NoError(t, err)

	res, err := s.Search(ctx, SearchOpts{})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, "new", res[0].Subject)
}

func TestSync_Idempotent(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	aid, _ := s.EnsureAccount(ctx, AccountSpec{Nickname: "p", Email: "m@x", IMAPHost: "h", IMAPPort: 993, IMAPTLS: true, Username: "u", SecretRef: "env:S"})
	acc := &fakeAccount{nickname: "p", uvs: map[string]uint32{"INBOX": 1}, envs: map[string][]Envelope{"INBOX": {{UID: 1, Subject: "x"}}}}
	_, err := Sync(ctx, s, aid, acc, []string{"INBOX"})
	require.NoError(t, err)
	_, err = Sync(ctx, s, aid, acc, []string{"INBOX"})
	require.NoError(t, err)
	res, _ := s.Search(ctx, SearchOpts{})
	require.Len(t, res, 1)
}
