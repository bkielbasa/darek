//go:build integration

package whatsapp

import (
	"context"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"

	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	pool := db.Wrap(raw)
	return &Manager{
		pool:   pool,
		store:  NewStore(pool),
		logger: waLog.Stdout("test", "WARN", true),
	}
}

func TestIngestMessage_DropsUnknownGroup(t *testing.T) {
	m := newManager(t)

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.JID{User: "g-new", Server: types.GroupServer},
				Sender: types.JID{User: "1", Server: types.DefaultUserServer},
			},
			ID:        "M1",
			Timestamp: time.Now().UTC(),
			PushName:  "Bart",
		},
		Message: &waE2E.Message{Conversation: strPtr("hi")},
	}
	m.ingestMessage(context.Background(), evt)

	groups, err := m.store.Groups(context.Background())
	require.NoError(t, err)
	require.Len(t, groups, 1, "unknown group is registered as disabled")
	require.False(t, groups[0].IngestEnabled)
	require.Equal(t, 0, groups[0].MessageCount, "message dropped because group not opted in")
}

func TestIngestMessage_StoresWhenEnabled(t *testing.T) {
	m := newManager(t)
	require.NoError(t, m.store.UpsertGroup(context.Background(), "g1@g.us", "G1"))
	require.NoError(t, m.store.SetIngestEnabled(context.Background(), "g1@g.us", true))

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.JID{User: "g1", Server: types.GroupServer},
				Sender: types.JID{User: "5", Server: types.DefaultUserServer},
			},
			ID:        "M1",
			Timestamp: time.Now().UTC(),
			PushName:  "Bart",
		},
		Message: &waE2E.Message{Conversation: strPtr("hello")},
	}
	m.ingestMessage(context.Background(), evt)

	groups, err := m.store.Groups(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, groups[0].MessageCount)
}
