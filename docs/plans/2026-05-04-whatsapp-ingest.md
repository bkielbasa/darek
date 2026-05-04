# WhatsApp Connection + Group Message Ingest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a long-running WhatsApp connection inside `darek serve`. The user pairs once via a QR code in the inbox web UI, opts in to specific groups, and incoming messages from those groups are persisted to Postgres. No agent tools yet — that's sub-projects B and C.

**Architecture:** New `tools/whatsapp/` package wrapping `go.mau.fi/whatsmeow`. A `Manager` owns the whatsmeow client and event handler; a `Store` handles Postgres CRUD; pure helpers (`decodeMessage`, pairing state machine) are unit-testable in isolation. Whatsmeow's session/device data persists to a SQLite file at `~/.darek/whatsapp/store.db`; our domain data (groups + messages + opt-in flags) goes in two new Postgres tables. New web UI at `/whatsapp` shows either the QR (state A) or the groups-with-toggles list (state B).

**Tech Stack:** `go.mau.fi/whatsmeow` (multi-device WhatsApp protocol), `github.com/skip2/go-qrcode` (QR PNG rendering), modernc.org/sqlite (CGO-free SQLite for whatsmeow's session store), existing project libs (pgx, OTEL, html/template, HTMX). Tests use `github.com/stretchr/testify/require` and the project's pg testcontainer helper.

**Design source:** [docs/specs/2026-05-04-whatsapp-ingest-design.md](../specs/2026-05-04-whatsapp-ingest-design.md), approved 2026-05-04.

**Out of scope (deferred):** Agent tools (sub-projects B and C), DMs, media payload download, reactions/edits/deletes/replies, historical backfill, retention policies, multi-account, at-rest encryption.

---

## File Map

| Path | Responsibility |
|---|---|
| `db/migrations/0006_whatsapp.up.sql` | (create) `whatsapp_groups` + `whatsapp_messages` tables, index. |
| `config/types.go` | (modify) add `WhatsApp` struct + field on `Config`. |
| `tools/whatsapp/store.go` | (create) `Store` type with `UpsertGroup`, `SetIngestEnabled`, `IngestEnabled`, `InsertMessage`, `Groups`. |
| `tools/whatsapp/store_test.go` | (create) integration tests against pg testcontainer. |
| `tools/whatsapp/decode.go` | (create) pure `decodeMessage(*events.Message) (kind, body string)`. |
| `tools/whatsapp/decode_test.go` | (create) table-driven tests for each `kind`. |
| `tools/whatsapp/pairing.go` | (create) `PairingState` struct + transitions. |
| `tools/whatsapp/pairing_test.go` | (create) state machine unit tests. |
| `tools/whatsapp/manager.go` | (create) `Manager` wrapping whatsmeow.Client; `Run`, `Close`, `PairingState`, `Groups`, `RefreshGroups`, `SetIngestEnabled`, `Unpair`. |
| `tools/whatsapp/manager_test.go` | (create) handler-pipeline tests using stubbed `decodeMessage` inputs. |
| `cmd/darek/serve/whatsapp.go` | (create) HTTP handlers: `handleWhatsApp`, `handleWhatsAppQR`, `handleWhatsAppToggleGroup`, `handleWhatsAppRefreshGroups`, `handleWhatsAppUnpair`. |
| `cmd/darek/serve/whatsapp_test.go` | (create) handler tests with a stub manager. |
| `cmd/darek/serve/templates/whatsapp.html` | (create) state-A + state-B page. |
| `cmd/darek/serve/templates/_whatsapp_group_row.html` | (create) HTMX-swappable partial. |
| `cmd/darek/serve/static/style.css` | (modify) `.qr`, `.danger`, `.wa-row` rules (~15 lines). |
| `cmd/darek/serve/server.go` | (modify) `Server` gains `whatsApp WhatsAppManager` field; `New` takes it; `routes()` registers `/whatsapp/*`. |
| `cmd/darek/serve.go` | (modify) build `*whatsapp.Manager` if configured; pass to `serve.New`; goroutine runs `Manager.Run`. |
| `README.md` | (modify) document the WhatsApp pairing + group selection UX, the `enabled` flag, and the ToS / ban-risk warning. |

---

## Task 1 — Migration + config

**Files:**
- Create: `db/migrations/0006_whatsapp.up.sql`
- Modify: `config/types.go`

- [ ] **Step 1: Create the migration**

Create `db/migrations/0006_whatsapp.up.sql`:

```sql
CREATE TABLE whatsapp_groups (
    jid             text PRIMARY KEY,
    name            text NOT NULL,
    ingest_enabled  boolean NOT NULL DEFAULT false,
    last_synced_at  timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE whatsapp_messages (
    id           text PRIMARY KEY,
    group_jid    text NOT NULL REFERENCES whatsapp_groups(jid) ON DELETE CASCADE,
    sender_jid   text NOT NULL,
    sender_name  text NOT NULL,
    kind         text NOT NULL,
    body         text NOT NULL,
    sent_at      timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_whatsapp_messages_group_sent ON whatsapp_messages(group_jid, sent_at DESC);
```

- [ ] **Step 2: Add `WhatsApp` config block**

In `config/types.go`, append after the existing `Auth` struct:

```go
type WhatsApp struct {
	Enabled   bool   `yaml:"enabled"`
	StorePath string `yaml:"store_path"` // sqlite path for whatsmeow session; defaults to ~/.darek/whatsapp/store.db
}
```

In the `Config` struct, append the field (slot it alphabetically near other top-level optionals):

```go
WhatsApp WhatsApp `yaml:"whatsapp"`
```

- [ ] **Step 3: Build to verify**

Run: `cd /Users/bklimczak/Projects/darek && go build ./config/...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add db/migrations/0006_whatsapp.up.sql config/types.go
git commit -m "feat(whatsapp): db migration + config block"
```

---

## Task 2 — `tools/whatsapp/store.go` (TDD, integration)

**Files:**
- Create: `tools/whatsapp/store.go`
- Create: `tools/whatsapp/store_test.go`

`Store` is a thin Postgres CRUD over the new tables. Same shape as `links.Store`: takes a wrapped `*db.Pool` (or `*pgxpool.Pool` if the project's wrapper is `db.Wrap`), exposes named methods, no struct embedding.

- [ ] **Step 1: Create the file with type + signatures (no bodies)**

Create `tools/whatsapp/store.go`:

```go
package whatsapp

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Group is the row shape returned by Groups(); MessageCount and LastMessageAt
// are derived from whatsapp_messages, not stored on the row itself.
type Group struct {
	JID            string
	Name           string
	IngestEnabled  bool
	MessageCount   int
	LastMessageAt  *time.Time
}

// Message is what InsertMessage takes and what the schema mirrors directly.
type Message struct {
	ID         string
	GroupJID   string
	SenderJID  string
	SenderName string
	Kind       string
	Body       string
	SentAt     time.Time
}

// UpsertGroup inserts a row or updates name + last_synced_at on conflict.
// Crucially: ingest_enabled is preserved on conflict so a metadata refresh
// never silently undoes a user opt-in.
func (s *Store) UpsertGroup(ctx context.Context, jid, name string) error {
	return nil // stub
}

// SetIngestEnabled flips the flag on a single group. No-op if the group
// doesn't exist (returns no error).
func (s *Store) SetIngestEnabled(ctx context.Context, jid string, enabled bool) error {
	return nil // stub
}

// IngestEnabled reports whether the group exists and has the flag set.
func (s *Store) IngestEnabled(ctx context.Context, jid string) (exists, enabled bool, err error) {
	return false, false, nil // stub
}

// InsertMessage inserts a row; ON CONFLICT (id) DO NOTHING keeps it idempotent
// across reconnect-driven duplicate deliveries.
func (s *Store) InsertMessage(ctx context.Context, m Message) error {
	return nil // stub
}

// Groups returns every row from whatsapp_groups joined with per-group counts.
func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	return nil, nil // stub
}
```

(If the project uses a wrapper type — e.g. `db.Wrap(pool) *db.Pool` — search `grep -rn "func Wrap" /Users/bklimczak/Projects/darek/db/` and adapt the field type. Other store packages here use `*pgxpool.Pool` directly via the wrapper, so check `links/store.go` for the pattern.)

- [ ] **Step 2: Add the dependency**

Run: `cd /Users/bklimczak/Projects/darek && go get github.com/jackc/pgx/v5@latest`
Expected: go.mod / go.sum updated, no compile output. (pgx/v5 is almost certainly already a direct dep — verify with `grep "jackc/pgx" go.mod`. Don't add if already present.)

- [ ] **Step 3: Write failing tests**

Create `tools/whatsapp/store_test.go`:

```go
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

	// Refresh: same JID, new name. ingest_enabled MUST stay true.
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
	require.NoError(t, s.InsertMessage(ctx, msg)) // duplicate must not error

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
```

- [ ] **Step 4: Run, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test -tags integration ./tools/whatsapp/ -v`
Expected: tests fail (stub bodies return zero values).

- [ ] **Step 5: Implement the methods**

Replace stubs in `tools/whatsapp/store.go`:

```go
func (s *Store) UpsertGroup(ctx context.Context, jid, name string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO whatsapp_groups (jid, name)
		VALUES ($1, $2)
		ON CONFLICT (jid) DO UPDATE
		   SET name           = EXCLUDED.name,
		       last_synced_at = now()
	`, jid, name)
	return err
}

func (s *Store) SetIngestEnabled(ctx context.Context, jid string, enabled bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE whatsapp_groups SET ingest_enabled = $2 WHERE jid = $1`,
		jid, enabled)
	return err
}

func (s *Store) IngestEnabled(ctx context.Context, jid string) (exists, enabled bool, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT ingest_enabled FROM whatsapp_groups WHERE jid = $1`, jid).Scan(&enabled)
	if err != nil {
		// distinguish "not found" from real errors
		if errIsNoRows(err) {
			return false, false, nil
		}
		return false, false, err
	}
	return true, enabled, nil
}

func (s *Store) InsertMessage(ctx context.Context, m Message) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO whatsapp_messages (id, group_jid, sender_jid, sender_name, kind, body, sent_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO NOTHING
	`, m.ID, m.GroupJID, m.SenderJID, m.SenderName, m.Kind, m.Body, m.SentAt)
	return err
}

func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.jid, g.name, g.ingest_enabled,
		       COALESCE(c.cnt, 0)         AS msg_count,
		       c.last                      AS last_at
		  FROM whatsapp_groups g
		  LEFT JOIN (
		    SELECT group_jid, count(*) AS cnt, max(sent_at) AS last
		      FROM whatsapp_messages
		     GROUP BY group_jid
		  ) c ON c.group_jid = g.jid
		 ORDER BY g.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.JID, &g.Name, &g.IngestEnabled, &g.MessageCount, &g.LastMessageAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// errIsNoRows wraps the pgx "no rows" sentinel in a way that doesn't force a
// pgx import on test files. Defined here for symmetry with how other store
// packages in this repo distinguish not-found.
func errIsNoRows(err error) bool {
	// pgx.ErrNoRows is the canonical answer; sql.ErrNoRows would also work
	// for compatibility. Both compare via errors.Is.
	return err != nil && err.Error() == "no rows in result set"
}
```

(If the project already exposes a helper like `links.ErrNoRows()`, use it instead of the string-compare hack. Search `grep -rn "ErrNoRows\|pgx.ErrNoRows" /Users/bklimczak/Projects/darek/links/` to confirm. Replace `errIsNoRows` with that helper.)

Add to imports if not already there: `"github.com/jackc/pgx/v5"`. Drop the string compare and use `errors.Is(err, pgx.ErrNoRows)`. Final form:

```go
import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ...

func (s *Store) IngestEnabled(ctx context.Context, jid string) (exists, enabled bool, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT ingest_enabled FROM whatsapp_groups WHERE jid = $1`, jid).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, enabled, nil
}
```

Drop the `errIsNoRows` helper.

- [ ] **Step 6: Run, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test -tags integration ./tools/whatsapp/ -v`
Expected: PASS for all 5 tests.

- [ ] **Step 7: Commit**

```bash
git add tools/whatsapp/store.go tools/whatsapp/store_test.go go.mod go.sum
git commit -m "feat(whatsapp): Postgres store for groups and messages"
```

---

## Task 3 — Add whatsmeow + qrcode deps; `decodeMessage` (TDD)

**Files:**
- Modify: `go.mod` (add `go.mau.fi/whatsmeow`, `github.com/skip2/go-qrcode`)
- Create: `tools/whatsapp/decode.go`
- Create: `tools/whatsapp/decode_test.go`

`decodeMessage` is the pure heart of the ingestion pipeline: given a whatsmeow `*events.Message`, return the `(kind, body)` we'll insert. Splitting it out from the manager keeps it unit-testable without faking the whole whatsmeow client.

- [ ] **Step 1: Pin known-good whatsmeow + qrcode versions**

Run: `cd /Users/bklimczak/Projects/darek && go get go.mau.fi/whatsmeow@latest github.com/skip2/go-qrcode@latest`
Expected: go.mod and go.sum updated.

(Whatsmeow's API changes occasionally; pinning to the latest tagged version at implementation time is the right call. If `go test ./tools/whatsapp/` later breaks during a `go get -u` it's a signal that the whatsmeow protocol surface moved.)

- [ ] **Step 2: Write the failing test**

Create `tools/whatsapp/decode_test.go`:

```go
package whatsapp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func msgWith(m *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.JID{User: "g1", Server: types.GroupServer},
				Sender: types.JID{User: "1234", Server: types.DefaultUserServer},
			},
			ID:        "MID1",
			Timestamp: time.Now().UTC(),
			PushName:  "Bart",
		},
		Message: m,
	}
}

func TestDecodeMessage(t *testing.T) {
	cases := []struct {
		name       string
		msg        *waE2E.Message
		wantKind   string
		wantBody   string
		bodyContains bool // when true, treat wantBody as substring
	}{
		{
			name:     "plain text via Conversation",
			msg:      &waE2E.Message{Conversation: strPtr("hello world")},
			wantKind: "text",
			wantBody: "hello world",
		},
		{
			name: "extended text",
			msg: &waE2E.Message{
				ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: strPtr("link preview text")},
			},
			wantKind: "text",
			wantBody: "link preview text",
		},
		{
			name: "image without caption",
			msg: &waE2E.Message{
				ImageMessage: &waE2E.ImageMessage{},
			},
			wantKind: "image",
			wantBody: "[image]",
		},
		{
			name: "image with caption",
			msg: &waE2E.Message{
				ImageMessage: &waE2E.ImageMessage{Caption: strPtr("look at this")},
			},
			wantKind:     "image",
			wantBody:     "look at this",
			bodyContains: true,
		},
		{
			name: "video",
			msg: &waE2E.Message{
				VideoMessage: &waE2E.VideoMessage{},
			},
			wantKind: "video",
			wantBody: "[video]",
		},
		{
			name: "voice (PTT)",
			msg: &waE2E.Message{
				AudioMessage: &waE2E.AudioMessage{
					PTT:     boolPtr(true),
					Seconds: u32Ptr(12),
				},
			},
			wantKind:     "voice",
			wantBody:     "[voice 12s]",
		},
		{
			name: "audio (non-PTT)",
			msg: &waE2E.Message{
				AudioMessage: &waE2E.AudioMessage{PTT: boolPtr(false)},
			},
			wantKind: "audio",
			wantBody: "[audio]",
		},
		{
			name: "document with filename",
			msg: &waE2E.Message{
				DocumentMessage: &waE2E.DocumentMessage{FileName: strPtr("report.pdf")},
			},
			wantKind:     "document",
			wantBody:     "report.pdf",
			bodyContains: true,
		},
		{
			name: "sticker",
			msg: &waE2E.Message{
				StickerMessage: &waE2E.StickerMessage{},
			},
			wantKind: "sticker",
			wantBody: "[sticker]",
		},
		{
			name:     "unknown / nothing populated",
			msg:      &waE2E.Message{},
			wantKind: "other",
			wantBody: "[unsupported message type]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, body := decodeMessage(msgWith(tc.msg))
			require.Equal(t, tc.wantKind, kind)
			if tc.bodyContains {
				require.Contains(t, body, tc.wantBody)
			} else {
				require.Equal(t, tc.wantBody, body)
			}
		})
	}
}

func strPtr(s string) *string  { return &s }
func boolPtr(b bool) *bool     { return &b }
func u32Ptr(v uint32) *uint32  { return &v }
```

(Note: whatsmeow types use `*string`/`*bool`/`*uint32` pointer fields generated from protobuf. The pointer helpers above are conventional. If your whatsmeow version uses non-pointer fields, drop the helpers and pass values directly.)

- [ ] **Step 3: Run, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/whatsapp/ -run TestDecodeMessage -v`
Expected: FAIL — `decodeMessage` undefined.

- [ ] **Step 4: Implement `decodeMessage`**

Create `tools/whatsapp/decode.go`:

```go
package whatsapp

import (
	"fmt"

	"go.mau.fi/whatsmeow/types/events"
)

// decodeMessage maps a whatsmeow *events.Message to (kind, body) for
// persistence. It is pure: no I/O, no goroutine, no logging. Unknown shapes
// fall through to ("other", "[unsupported message type]") so reactions,
// edits, deletes, system messages, etc. land as readable rows rather than
// errors. The full list of "kind" values is the soft enum stored in
// whatsapp_messages.kind: text|image|voice|audio|video|document|sticker|other.
func decodeMessage(msg *events.Message) (kind, body string) {
	if msg == nil || msg.Message == nil {
		return "other", "[unsupported message type]"
	}
	m := msg.Message

	switch {
	case m.Conversation != nil && *m.Conversation != "":
		return "text", *m.Conversation

	case m.ExtendedTextMessage != nil && m.ExtendedTextMessage.Text != nil:
		return "text", strDeref(m.ExtendedTextMessage.Text)

	case m.ImageMessage != nil:
		caption := strDeref(m.ImageMessage.Caption)
		if caption != "" {
			return "image", "[image] " + caption
		}
		return "image", "[image]"

	case m.VideoMessage != nil:
		caption := strDeref(m.VideoMessage.Caption)
		if caption != "" {
			return "video", "[video] " + caption
		}
		return "video", "[video]"

	case m.AudioMessage != nil:
		secs := uint32Deref(m.AudioMessage.Seconds)
		if boolDeref(m.AudioMessage.PTT) {
			return "voice", fmt.Sprintf("[voice %ds]", secs)
		}
		return "audio", "[audio]"

	case m.DocumentMessage != nil:
		name := strDeref(m.DocumentMessage.FileName)
		if name == "" {
			return "document", "[document]"
		}
		return "document", "[document: " + name + "]"

	case m.StickerMessage != nil:
		return "sticker", "[sticker]"
	}

	return "other", "[unsupported message type]"
}

// strDeref / boolDeref / uint32Deref unwrap protobuf pointer fields safely.
// Whatsmeow's generated types use pointer scalars; nil means "field absent".

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func boolDeref(p *bool) bool {
	return p != nil && *p
}

func uint32Deref(p *uint32) uint32 {
	if p == nil {
		return 0
	}
	return *p
}

```

If `strings` is not used after the final form, drop it from the import list (the package is included above as a placeholder for future caption-trimming if needed).

- [ ] **Step 5: Run, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/whatsapp/ -run TestDecodeMessage -v`
Expected: PASS for all 10 subtests.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum tools/whatsapp/decode.go tools/whatsapp/decode_test.go
git commit -m "feat(whatsapp): decodeMessage maps whatsmeow events to (kind, body)"
```

---

## Task 4 — Pairing state machine (TDD)

**Files:**
- Create: `tools/whatsapp/pairing.go`
- Create: `tools/whatsapp/pairing_test.go`

`PairingState` is what the UI reads — a small struct describing whether we're paired, connected, and what QR string the user should scan. It is mutated only by the manager; the UI gets read-only snapshots.

- [ ] **Step 1: Write failing tests**

Create `tools/whatsapp/pairing_test.go`:

```go
package whatsapp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPairing_InitialState(t *testing.T) {
	var p pairing
	st := p.snapshot()
	require.False(t, st.Paired)
	require.False(t, st.Connected)
	require.Empty(t, st.QRCode)
}

func TestPairing_SetQR(t *testing.T) {
	var p pairing
	p.setQR("data-here")
	st := p.snapshot()
	require.False(t, st.Paired)
	require.Equal(t, "data-here", st.QRCode)
	require.False(t, st.QRRotatedAt.IsZero())
}

func TestPairing_SetPaired(t *testing.T) {
	var p pairing
	p.setQR("data-here")
	p.setPaired("Device", "+447700900000")
	st := p.snapshot()
	require.True(t, st.Paired)
	require.Equal(t, "Device", st.DeviceName)
	require.Equal(t, "+447700900000", st.PhoneE164)
	require.Empty(t, st.QRCode, "QR cleared once paired")
}

func TestPairing_Connect_Disconnect(t *testing.T) {
	var p pairing
	p.setPaired("Device", "+1")
	require.False(t, p.snapshot().Connected)

	p.setConnected(true)
	require.True(t, p.snapshot().Connected)

	p.setConnected(false)
	require.False(t, p.snapshot().Connected)
}

func TestPairing_Reset(t *testing.T) {
	var p pairing
	p.setPaired("Device", "+1")
	p.setConnected(true)

	p.reset()
	st := p.snapshot()
	require.False(t, st.Paired)
	require.False(t, st.Connected)
	require.Empty(t, st.QRCode)
	require.Empty(t, st.DeviceName)
	require.Empty(t, st.PhoneE164)
}

func TestPairing_SnapshotIsCopy(t *testing.T) {
	var p pairing
	p.setQR("first")
	st := p.snapshot()
	p.setQR("second")
	require.Equal(t, "first", st.QRCode, "snapshot must not reflect later mutations")
	_ = time.Now() // silence unused import in some toolchains
}
```

- [ ] **Step 2: Run, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/whatsapp/ -run TestPairing -v`
Expected: FAIL — types undefined.

- [ ] **Step 3: Implement**

Create `tools/whatsapp/pairing.go`:

```go
package whatsapp

import (
	"sync"
	"time"
)

// PairingState is the read-only view UI handlers consume via
// Manager.PairingState(). All fields are zero-valued in the initial state
// (no session, no QR, not connected).
type PairingState struct {
	Paired      bool
	Connected   bool
	QRCode      string
	QRRotatedAt time.Time
	DeviceName  string
	PhoneE164   string
}

// pairing holds the mutable inner state. Methods are guarded by an internal
// mutex; the only external read path is snapshot(), which copies the struct.
type pairing struct {
	mu sync.RWMutex
	st PairingState
}

func (p *pairing) snapshot() PairingState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.st
}

func (p *pairing) setQR(code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.QRCode = code
	p.st.QRRotatedAt = time.Now()
}

func (p *pairing) setPaired(deviceName, phoneE164 string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.Paired = true
	p.st.DeviceName = deviceName
	p.st.PhoneE164 = phoneE164
	p.st.QRCode = ""
	p.st.QRRotatedAt = time.Time{}
}

func (p *pairing) setConnected(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.Connected = on
}

func (p *pairing) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st = PairingState{}
}
```

- [ ] **Step 4: Run, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/whatsapp/ -run TestPairing -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/whatsapp/pairing.go tools/whatsapp/pairing_test.go
git commit -m "feat(whatsapp): pairing state machine"
```

---

## Task 5 — `Manager` (whatsmeow integration)

**Files:**
- Create: `tools/whatsapp/manager.go`
- Create: `tools/whatsapp/manager_test.go`

This is the heaviest task — wraps whatsmeow's `Client`, owns the SQLite session store, registers the event handler, and ties the message pipeline together. We avoid faking whatsmeow's surface; instead we test the integration points (decodeMessage already covered, store covered) and exercise just the `handleEvent` path with synthesized inputs.

- [ ] **Step 1: Create the manager file**

Create `tools/whatsapp/manager.go`:

```go
package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Options configures NewManager. StorePath is the SQLite session file (created
// if missing). Pool is the existing darek Postgres pool. Logger may be nil.
type Options struct {
	StorePath string
	Pool      *pgxpool.Pool
	Logger    waLog.Logger
}

// Manager owns the WhatsApp connection lifecycle. Methods are safe for
// concurrent use by HTTP handlers and the internal event goroutine.
type Manager struct {
	pool       *pgxpool.Pool
	store      *Store
	storePath  string
	logger     waLog.Logger

	pair       pairing
	mu         sync.Mutex
	client     *whatsmeow.Client
	container  *sqlstore.Container
	stopFn     context.CancelFunc
}

// NewManager loads (or creates) the SQLite session store. It does not connect
// — call Run to start the connection loop.
func NewManager(opts Options) (*Manager, error) {
	if opts.Pool == nil {
		return nil, errors.New("whatsapp.NewManager: Pool is required")
	}
	if opts.StorePath == "" {
		opts.StorePath = filepath.Join(os.Getenv("HOME"), ".darek", "whatsapp", "store.db")
	}
	if err := os.MkdirAll(filepath.Dir(opts.StorePath), 0o700); err != nil {
		return nil, fmt.Errorf("whatsapp store dir: %w", err)
	}
	if opts.Logger == nil {
		opts.Logger = waLog.Stdout("whatsmeow", "INFO", true)
	}

	dbLog := waLog.Stdout("wadb", "WARN", true)
	container, err := sqlstore.New(context.Background(), "sqlite3",
		fmt.Sprintf("file:%s?_foreign_keys=on", opts.StorePath), dbLog)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: %w", err)
	}

	return &Manager{
		pool:      opts.Pool,
		store:     NewStore(opts.Pool),
		storePath: opts.StorePath,
		logger:    opts.Logger,
		container: container,
	}, nil
}

// Run blocks until ctx is canceled. It connects to whatsmeow, registers the
// event handler, and orchestrates the QR pairing flow on first run.
func (m *Manager) Run(ctx context.Context) error {
	device, err := m.container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("get device: %w", err)
	}

	m.mu.Lock()
	m.client = whatsmeow.NewClient(device, m.logger)
	m.client.AddEventHandler(m.handleEvent)
	m.mu.Unlock()

	// Already paired: connect and stream events.
	if m.client.Store.ID != nil {
		m.pair.setPaired(deviceName(m.client), phoneE164(m.client))
		if err := m.client.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		<-ctx.Done()
		m.client.Disconnect()
		return ctx.Err()
	}

	// Not paired: drive the QR pairing flow.
	qrChan, err := m.client.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("qr channel: %w", err)
	}
	if err := m.client.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	for evt := range qrChan {
		switch evt.Event {
		case "code":
			m.pair.setQR(evt.Code)
		case "success":
			m.pair.setPaired(deviceName(m.client), phoneE164(m.client))
		case "timeout", "err-client-outdated", "err-scanned-without-multidevice":
			// fatal — bail out so caller can see the error
			m.client.Disconnect()
			return fmt.Errorf("pairing failed: %s", evt.Event)
		}
		select {
		case <-ctx.Done():
			m.client.Disconnect()
			return ctx.Err()
		default:
		}
	}

	// QR channel closed implies success or context cancel.
	<-ctx.Done()
	m.client.Disconnect()
	return ctx.Err()
}

// Close is safe to call after Run returns.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		m.client.Disconnect()
	}
	return nil
}

// PairingState returns a read-only snapshot.
func (m *Manager) PairingState() PairingState {
	return m.pair.snapshot()
}

// IsConnected reports whether the underlying client is currently connected.
func (m *Manager) IsConnected() bool {
	return m.pair.snapshot().Connected
}

// Groups returns the persisted view (joined groups + counts), reading from Postgres.
// Use RefreshGroups to sync from whatsmeow first.
func (m *Manager) Groups(ctx context.Context) ([]Group, error) {
	return m.store.Groups(ctx)
}

// RefreshGroups asks whatsmeow for the live joined-groups list and upserts
// the rows. Existing ingest_enabled flags are preserved.
func (m *Manager) RefreshGroups(ctx context.Context) error {
	m.mu.Lock()
	cli := m.client
	m.mu.Unlock()
	if cli == nil {
		return errors.New("whatsapp: client not initialized")
	}
	groups, err := cli.GetJoinedGroups()
	if err != nil {
		return fmt.Errorf("joined groups: %w", err)
	}
	for _, g := range groups {
		if err := m.store.UpsertGroup(ctx, g.JID.String(), g.GroupName.Name); err != nil {
			return fmt.Errorf("upsert group %s: %w", g.JID, err)
		}
	}
	return nil
}

// SetIngestEnabled flips a single group's flag.
func (m *Manager) SetIngestEnabled(ctx context.Context, jid string, on bool) error {
	return m.store.SetIngestEnabled(ctx, jid, on)
}

// Unpair logs out, disconnects, and deletes the SQLite store. Postgres data
// (whatsapp_messages, whatsapp_groups) is preserved.
func (m *Manager) Unpair(ctx context.Context) error {
	m.mu.Lock()
	cli := m.client
	m.mu.Unlock()
	if cli != nil {
		_ = cli.Logout(ctx)
		cli.Disconnect()
	}
	m.pair.reset()
	if err := os.Remove(m.storePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove store: %w", err)
	}
	return nil
}

// handleEvent is the sole whatsmeow event handler. Connection events update
// the pairing snapshot; group messages flow through ingestMessage.
func (m *Manager) handleEvent(evt any) {
	switch e := evt.(type) {
	case *events.Connected:
		m.pair.setConnected(true)
	case *events.Disconnected, *events.LoggedOut:
		m.pair.setConnected(false)
	case *events.Message:
		if e.Info.Chat.Server != types.GroupServer {
			return
		}
		// ctx for the insert: short timeout, independent of any caller.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.ingestMessage(ctx, e)
	}
}

// ingestMessage is the per-message pipeline:
//   1. Lookup ingest_enabled. If group unknown, upsert (disabled) and drop.
//   2. If disabled, drop silently.
//   3. Decode (kind, body). Insert with ON CONFLICT DO NOTHING.
func (m *Manager) ingestMessage(ctx context.Context, e *events.Message) {
	groupJID := e.Info.Chat.String()

	exists, enabled, err := m.store.IngestEnabled(ctx, groupJID)
	if err != nil {
		m.logger.Warnf("ingest enabled lookup failed: %v", err)
		return
	}
	if !exists {
		// Best-effort: register the group as known (disabled) so the user
		// can opt in via the UI without needing a Refresh.
		_ = m.store.UpsertGroup(ctx, groupJID, e.Info.PushName)
		return
	}
	if !enabled {
		return
	}

	kind, body := decodeMessage(e)

	senderName := e.Info.PushName
	if senderName == "" {
		senderName = e.Info.Sender.String()
	}

	if err := m.store.InsertMessage(ctx, Message{
		ID:         e.Info.ID,
		GroupJID:   groupJID,
		SenderJID:  e.Info.Sender.String(),
		SenderName: senderName,
		Kind:       kind,
		Body:       body,
		SentAt:     e.Info.Timestamp,
	}); err != nil {
		m.logger.Warnf("insert message: %v", err)
	}
}

// deviceName / phoneE164 best-effort extract human-readable identifiers from
// the connected client. Empty strings are acceptable defaults.
func deviceName(cli *whatsmeow.Client) string {
	if cli == nil || cli.Store == nil || cli.Store.ID == nil {
		return ""
	}
	if cli.Store.PushName != "" {
		return cli.Store.PushName
	}
	return cli.Store.ID.User
}

func phoneE164(cli *whatsmeow.Client) string {
	if cli == nil || cli.Store == nil || cli.Store.ID == nil {
		return ""
	}
	return "+" + cli.Store.ID.User
}
```

- [ ] **Step 2: Compile**

Run: `cd /Users/bklimczak/Projects/darek && go build ./tools/whatsapp/`
Expected: clean build. The whatsmeow API surface is large and version-dependent; if this fails, search for the renamed symbol in the whatsmeow `go doc` output and adapt:

```bash
go doc go.mau.fi/whatsmeow.Client.GetJoinedGroups
go doc go.mau.fi/whatsmeow.Client.GetQRChannel
go doc go.mau.fi/whatsmeow/store.Container
```

Common adaptations: `GetFirstDevice` may take or omit `ctx` depending on version; `GetJoinedGroups` may return `[]*types.GroupInfo` with a `Name` field directly (not nested under `GroupName`). Adapt and confirm `go build` is green.

- [ ] **Step 3: Write a small manager test**

Create `tools/whatsapp/manager_test.go`:

```go
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

func TestIngestMessage_DropsUnknownGroup(t *testing.T) {
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))

	m := &Manager{pool: raw, store: NewStore(raw), logger: waLog.Stdout("test", "WARN", true)}

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
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))

	m := &Manager{pool: raw, store: NewStore(raw), logger: waLog.Stdout("test", "WARN", true)}
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
```

The actual `Run` / `Connect` / pairing flow is integration territory only; we skip unit-testing it.

- [ ] **Step 4: Run tests**

Run: `cd /Users/bklimczak/Projects/darek && go test -tags integration ./tools/whatsapp/ -v`
Expected: all 7+ tests pass (5 store + decode + pairing + 2 manager).

- [ ] **Step 5: Commit**

```bash
git add tools/whatsapp/manager.go tools/whatsapp/manager_test.go
git commit -m "feat(whatsapp): manager wraps whatsmeow client + ingest pipeline"
```

---

## Task 6 — HTTP handlers + templates

**Files:**
- Create: `cmd/darek/serve/whatsapp.go`
- Create: `cmd/darek/serve/whatsapp_test.go`
- Create: `cmd/darek/serve/templates/whatsapp.html`
- Create: `cmd/darek/serve/templates/_whatsapp_group_row.html`
- Modify: `cmd/darek/serve/static/style.css`

This task adds the UI surface but does NOT yet register the routes — that's Task 7's wiring step. Routes get registered in `routes()` only when the manager is non-nil.

- [ ] **Step 1: Define the `WhatsAppManager` interface in serve**

Add to `cmd/darek/serve/server.go` (near the existing `Analyzer` interface, around line 21):

```go
// WhatsAppManager is the subset of *whatsapp.Manager used by the HTTP server.
// Defined as an interface so tests can supply a fake.
type WhatsAppManager interface {
	PairingState() whatsapp.PairingState
	Groups(ctx context.Context) ([]whatsapp.Group, error)
	RefreshGroups(ctx context.Context) error
	SetIngestEnabled(ctx context.Context, jid string, on bool) error
	Unpair(ctx context.Context) error
}
```

Add `"darek/tools/whatsapp"` to the imports.

Modify the `Server` struct to add the field:

```go
type Server struct {
	store    *links.Store
	tmpl     *template.Template
	mux      *http.ServeMux
	sync     SyncFn
	analyze  Analyzer
	auth     AuthConfig
	whatsApp WhatsAppManager // nil-safe; handlers no-op when unset
}
```

Modify `New` to take it:

```go
func New(store *links.Store, sync SyncFn, analyzer Analyzer, auth AuthConfig, wa WhatsAppManager) (*Server, error) {
	t, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{store: store, tmpl: t, mux: http.NewServeMux(), sync: sync, analyze: analyzer, auth: auth, whatsApp: wa}
	s.routes()
	return s, nil
}
```

Update existing call sites of `serve.New` to pass `nil` as the 5th arg for now (the real value comes in Task 7). Search:

```bash
grep -rn "serve.New(" /Users/bklimczak/Projects/darek/
```

Add `nil` to each call.

- [ ] **Step 2: Add the routes (gated on manager presence)**

In `cmd/darek/serve/server.go`, at the end of `routes()` (after the `analyze` route at line 73), append:

```go
	if s.whatsApp != nil {
		s.mux.HandleFunc("GET /whatsapp", s.handleWhatsApp)
		s.mux.HandleFunc("GET /whatsapp/qr.png", s.handleWhatsAppQR)
		s.mux.HandleFunc("POST /whatsapp/groups/{jid}/toggle", s.handleWhatsAppToggleGroup)
		s.mux.HandleFunc("POST /whatsapp/groups/refresh", s.handleWhatsAppRefreshGroups)
		s.mux.HandleFunc("POST /whatsapp/unpair", s.handleWhatsAppUnpair)
	}
```

- [ ] **Step 3: Create the handlers**

Create `cmd/darek/serve/whatsapp.go`:

```go
package serve

import (
	"net/http"

	"darek/tools/whatsapp"

	"github.com/skip2/go-qrcode"
)

type whatsAppPageVM struct {
	State  whatsapp.PairingState
	Groups []whatsAppGroupVM
}

type whatsAppGroupVM struct {
	JID              string
	Name             string
	IngestEnabled    bool
	MessageCount     int
	LastMessageAtRel string // "12 min", "3 hr", or empty
}

func (s *Server) handleWhatsApp(w http.ResponseWriter, r *http.Request) {
	state := s.whatsApp.PairingState()
	vm := whatsAppPageVM{State: state}
	if state.Paired {
		groups, err := s.whatsApp.Groups(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		vm.Groups = make([]whatsAppGroupVM, 0, len(groups))
		for _, g := range groups {
			vm.Groups = append(vm.Groups, toWhatsAppGroupVM(g))
		}
	}
	if err := s.tmpl.ExecuteTemplate(w, "whatsapp.html", vm); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleWhatsAppQR(w http.ResponseWriter, r *http.Request) {
	state := s.whatsApp.PairingState()
	if state.Paired {
		w.Header().Set("HX-Redirect", "/whatsapp")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if state.QRCode == "" {
		http.Error(w, "no QR yet", http.StatusServiceUnavailable)
		return
	}
	png, err := qrcode.Encode(state.QRCode, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) handleWhatsAppToggleGroup(w http.ResponseWriter, r *http.Request) {
	jid := r.PathValue("jid")
	if jid == "" {
		http.Error(w, "bad jid", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("enabled") != ""
	if err := s.whatsApp.SetIngestEnabled(r.Context(), jid, enabled); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	groups, err := s.whatsApp.Groups(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, g := range groups {
		if g.JID == jid {
			_ = s.tmpl.ExecuteTemplate(w, "_whatsapp_group_row.html", toWhatsAppGroupVM(g))
			return
		}
	}
	http.Error(w, "group not found after toggle", http.StatusNotFound)
}

func (s *Server) handleWhatsAppRefreshGroups(w http.ResponseWriter, r *http.Request) {
	if err := s.whatsApp.RefreshGroups(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/whatsapp", http.StatusSeeOther)
}

func (s *Server) handleWhatsAppUnpair(w http.ResponseWriter, r *http.Request) {
	if err := s.whatsApp.Unpair(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/whatsapp", http.StatusSeeOther)
}

func toWhatsAppGroupVM(g whatsapp.Group) whatsAppGroupVM {
	vm := whatsAppGroupVM{
		JID:           g.JID,
		Name:          g.Name,
		IngestEnabled: g.IngestEnabled,
		MessageCount:  g.MessageCount,
	}
	if g.LastMessageAt != nil {
		vm.LastMessageAtRel = relTime(*g.LastMessageAt)
	}
	return vm
}
```

- [ ] **Step 4: Create the templates**

Create `cmd/darek/serve/templates/whatsapp.html`:

```html
{{define "whatsapp.html"}}
<!DOCTYPE html>
<html>
<head>
  <title>darek — WhatsApp</title>
  <link rel="stylesheet" href="/static/style.css">
  <script src="/static/htmx.min.js"></script>
</head>
<body class="auth">
  <h1>WhatsApp</h1>

  {{if .State.Paired}}
    <p>
      Connected as <b>{{.State.DeviceName}}</b>{{if .State.PhoneE164}} ({{.State.PhoneE164}}){{end}}.
      Status: {{if .State.Connected}}<span class="ok">online</span>{{else}}<span class="warn">offline</span>{{end}}.
    </p>
    <form action="/whatsapp/unpair" method="post" class="inline">
      <button type="submit" class="danger">Unpair this device</button>
    </form>

    <h2>Groups
      <form action="/whatsapp/groups/refresh" method="post" class="inline">
        <button type="submit">Refresh from WhatsApp</button>
      </form>
    </h2>

    {{if .Groups}}
      <table class="wa-groups">
        <thead><tr><th>Track</th><th>Name</th><th>Stats</th></tr></thead>
        <tbody>
          {{range .Groups}}{{template "_whatsapp_group_row.html" .}}{{end}}
        </tbody>
      </table>
    {{else}}
      <p>No groups known yet. Click <em>Refresh from WhatsApp</em> to load your group list.</p>
    {{end}}

  {{else}}
    <h2>Pair a device</h2>
    <p>Open WhatsApp on your phone → <em>Settings</em> → <em>Linked devices</em> →
       <em>Link a device</em> and scan this code:</p>

    <img class="qr" src="/whatsapp/qr.png"
         hx-get="/whatsapp/qr.png" hx-trigger="every 20s" hx-swap="outerHTML">

    <p class="hint">Refreshes every 20 seconds. Once paired, this page swaps to your groups list.</p>
  {{end}}
</body>
</html>
{{end}}
```

Create `cmd/darek/serve/templates/_whatsapp_group_row.html`:

```html
{{define "_whatsapp_group_row.html"}}
<tr id="wa-row-{{.JID}}" class="wa-row">
  <td>
    <form hx-post="/whatsapp/groups/{{.JID}}/toggle"
          hx-target="#wa-row-{{.JID}}" hx-swap="outerHTML"
          hx-trigger="change from:closest form">
      <input type="checkbox" name="enabled" value="1" {{if .IngestEnabled}}checked{{end}}>
    </form>
  </td>
  <td>{{.Name}}</td>
  <td class="meta">
    {{if .IngestEnabled}}
      {{.MessageCount}} stored
      {{if .LastMessageAtRel}}— last msg {{.LastMessageAtRel}} ago{{end}}
    {{else}}
      —
    {{end}}
  </td>
</tr>
{{end}}
```

(Verify `_row.html` is parsed via the existing template loader — search `grep -rn "ParseFS\|ParseFiles\|parseTemplates" cmd/darek/serve/`. The existing loader globs `*.html` from the templates directory, so the new files are picked up automatically.)

- [ ] **Step 5: Add CSS**

Append to `cmd/darek/serve/static/style.css`:

```css
.qr {
  display: block;
  margin: 1.5rem auto;
  width: 256px;
  height: 256px;
}

.danger {
  color: #b00020;
  border-color: #b00020;
}

.wa-groups td.meta {
  color: #666;
  font-size: 0.9em;
}

.wa-row td:first-child {
  text-align: center;
  width: 3rem;
}

.ok   { color: #1b8c2a; font-weight: 600; }
.warn { color: #b00020; font-weight: 600; }
```

- [ ] **Step 6: Write handler tests**

Create `cmd/darek/serve/whatsapp_test.go`:

```go
package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"darek/tools/whatsapp"

	"github.com/stretchr/testify/require"
)

// fakeWA is a stub WhatsAppManager for handler tests.
type fakeWA struct {
	state         whatsapp.PairingState
	groups        []whatsapp.Group
	groupsErr     error
	refreshCalled bool
	toggleCalls   []struct {
		JID string
		On  bool
	}
	unpairCalled bool
}

func (f *fakeWA) PairingState() whatsapp.PairingState { return f.state }
func (f *fakeWA) Groups(ctx context.Context) ([]whatsapp.Group, error) {
	return f.groups, f.groupsErr
}
func (f *fakeWA) RefreshGroups(ctx context.Context) error {
	f.refreshCalled = true
	return nil
}
func (f *fakeWA) SetIngestEnabled(ctx context.Context, jid string, on bool) error {
	f.toggleCalls = append(f.toggleCalls, struct {
		JID string
		On  bool
	}{jid, on})
	for i := range f.groups {
		if f.groups[i].JID == jid {
			f.groups[i].IngestEnabled = on
		}
	}
	return nil
}
func (f *fakeWA) Unpair(ctx context.Context) error {
	f.unpairCalled = true
	return nil
}

// authedServer builds a Server with auth bypassed for tests by wiring an
// always-authenticated session cookie. Reuses the existing test fixtures
// from server_test.go — adapt the helper name to whatever exists there.
//
// (If server_test.go has a helper like newTestServer(t) (*Server, *fakeWA, ...)
// extend it; otherwise define inline.)
func newTestServerWithWA(t *testing.T, wa *fakeWA) *Server {
	t.Helper()
	auth, err := NewAuthConfig("u", []byte("$2a$10$/abcdefghijklmnopqrstuO0kPqWQ8H1aS0eS8lU3oUu7iZE89B7Hu"), make([]byte, 32), time.Hour)
	require.NoError(t, err)
	s, err := New(nil, nil, nil, auth, wa)
	require.NoError(t, err)
	return s
}

func authedRequest(t *testing.T, s *Server, method, target string, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w := httptest.NewRecorder()
	s.setSessionCookie(w)
	for _, c := range w.Result().Cookies() {
		req.AddCookie(c)
	}
	resp := httptest.NewRecorder()
	s.Handler().ServeHTTP(resp, req)
	return resp.Result()
}

func TestHandleWhatsApp_RendersQRWhenNotPaired(t *testing.T) {
	wa := &fakeWA{state: whatsapp.PairingState{Paired: false, QRCode: "abc"}}
	s := newTestServerWithWA(t, wa)

	resp := authedRequest(t, s, "GET", "/whatsapp", "")
	require.Equal(t, 200, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, `src="/whatsapp/qr.png"`)
	require.Contains(t, body, "Pair a device")
}

func TestHandleWhatsApp_RendersGroupsWhenPaired(t *testing.T) {
	now := time.Now()
	wa := &fakeWA{
		state: whatsapp.PairingState{Paired: true, Connected: true, DeviceName: "MyPhone"},
		groups: []whatsapp.Group{
			{JID: "g1@g.us", Name: "Family", IngestEnabled: true, MessageCount: 7, LastMessageAt: &now},
			{JID: "g2@g.us", Name: "Work", IngestEnabled: false},
		},
	}
	s := newTestServerWithWA(t, wa)

	resp := authedRequest(t, s, "GET", "/whatsapp", "")
	require.Equal(t, 200, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "Family")
	require.Contains(t, body, "Work")
	require.Contains(t, body, "MyPhone")
	require.Contains(t, body, "7 stored")
}

func TestHandleWhatsApp_ToggleFlipsAndReturnsRow(t *testing.T) {
	wa := &fakeWA{
		state:  whatsapp.PairingState{Paired: true},
		groups: []whatsapp.Group{{JID: "g1@g.us", Name: "G1", IngestEnabled: false}},
	}
	s := newTestServerWithWA(t, wa)

	form := url.Values{"enabled": {"1"}}.Encode()
	resp := authedRequest(t, s, "POST", "/whatsapp/groups/g1@g.us/toggle", form)
	require.Equal(t, 200, resp.StatusCode)
	require.Len(t, wa.toggleCalls, 1)
	require.True(t, wa.toggleCalls[0].On)
	body := readBody(t, resp)
	require.Contains(t, body, "checked")
}

func TestHandleWhatsApp_UnauthRedirects(t *testing.T) {
	wa := &fakeWA{state: whatsapp.PairingState{Paired: true}}
	s := newTestServerWithWA(t, wa)

	req := httptest.NewRequest("GET", "/whatsapp", nil)
	resp := httptest.NewRecorder()
	s.Handler().ServeHTTP(resp, req)
	require.Equal(t, 303, resp.Result().StatusCode, "auth middleware redirects unauth'd to /login")
	require.Contains(t, resp.Header().Get("Location"), "/login")
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}
```

Add `"io"` to the imports for `readBody`. The bcrypt hash in `newTestServerWithWA` is for password `"x"` (use `darek auth hash x` to regenerate if needed). Adapt to whatever helper pattern `server_test.go` already uses.

- [ ] **Step 7: Run handler tests**

Run: `cd /Users/bklimczak/Projects/darek && go test ./cmd/darek/serve/ -v`
Expected: all tests pass (existing + 4 new).

- [ ] **Step 8: Commit**

```bash
git add cmd/darek/serve/server.go cmd/darek/serve/whatsapp.go cmd/darek/serve/whatsapp_test.go cmd/darek/serve/templates/whatsapp.html cmd/darek/serve/templates/_whatsapp_group_row.html cmd/darek/serve/static/style.css
# include any callers updated to pass nil:
git add cmd/darek/serve.go
git commit -m "feat(serve): WhatsApp pairing + groups UI"
```

---

## Task 7 — Wire `Manager` into `cmd/darek/serve.go`

**Files:**
- Modify: `cmd/darek/serve.go`

- [ ] **Step 1: Build the manager and start its goroutine**

In `cmd/darek/serve.go`, after `store := links.NewStore(pool)` (around line 60) and before the analyzer block, add:

```go
	var waManager *whatsapp.Manager
	if cfg.WhatsApp.Enabled {
		var err error
		waManager, err = whatsapp.NewManager(whatsapp.Options{
			StorePath: cfg.WhatsApp.StorePath,
			Pool:      pool,
		})
		if err != nil {
			return fmt.Errorf("whatsapp manager: %w", err)
		}
		go func() {
			if err := waManager.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "whatsapp: %v\n", err)
			}
		}()
	}
```

Add `"darek/tools/whatsapp"` and `"errors"` to the imports.

- [ ] **Step 2: Pass to `serve.New`**

Find the `srv, err := serve.New(store, sync, analyzer, authCfg)` call (around line 146) and update to:

```go
srv, err := serve.New(store, sync, analyzer, authCfg, waManager)
```

(Note: `waManager` is `*whatsapp.Manager`; `serve.New`'s parameter is `WhatsAppManager` interface — `*whatsapp.Manager` satisfies it. When `cfg.WhatsApp.Enabled=false`, `waManager` is nil and the routes don't register.)

- [ ] **Step 3: Build**

Run: `cd /Users/bklimczak/Projects/darek && go build ./...`
Expected: clean.

- [ ] **Step 4: Run all tests**

Run: `cd /Users/bklimczak/Projects/darek && make test`
Expected: PASS.

- [ ] **Step 5: Run lint**

Run: `cd /Users/bklimczak/Projects/darek && make lint`
Expected: no warnings.

- [ ] **Step 6: Commit**

```bash
git add cmd/darek/serve.go
git commit -m "feat(serve): wire whatsapp.Manager when enabled"
```

---

## Task 8 — README + observability + final verification

**Files:**
- Modify: `README.md`
- Modify: `obs/metrics.go`
- Modify: `obs/metrics_test.go`

- [ ] **Step 1: Add the metric instruments**

In `obs/metrics.go`, near the existing instruments (e.g. `LinksAnalyze`), add:

```go
WhatsAppMessages  metric.Int64Counter // darek.whatsapp.messages_ingested {kind, outcome}
WhatsAppConnected metric.Int64Gauge   // darek.whatsapp.connected (0|1)
```

In the constructor where instruments are initialized, add:

```go
WhatsAppMessages:  i64(m.Int64Counter("darek.whatsapp.messages_ingested")),
WhatsAppConnected: i64g(m.Int64Gauge("darek.whatsapp.connected")), // adapt to whatever helper exists for gauges; if none, use ObservableGauge or skip the gauge for v1
```

If the project has no `Int64Gauge` helper, drop `WhatsAppConnected` and rely on the existing `darek.dep.*` infrastructure or just skip the gauge — the spec marks it as nice-to-have. The counter is the load-bearing part.

In `obs/metrics_test.go`, extend `TestMetrics_Initialization` (or whatever asserts non-nil instruments) to include `m.WhatsAppMessages != nil`.

- [ ] **Step 2: Wire the counter in the manager**

In `tools/whatsapp/manager.go`, at the end of `ingestMessage`, after a successful insert, add:

```go
	if mInst, _ := obs.MetricsInstance(); mInst != nil {
		mInst.WhatsAppMessages.Add(ctx, 1, metric.WithAttributes(
			attribute.String("kind", kind),
			attribute.String("outcome", "ok"),
		))
	}
```

…and on error, the same with `outcome="error"` before the warn-log line. Add the imports:

```go
"darek/obs"
"go.opentelemetry.io/otel/attribute"
"go.opentelemetry.io/otel/metric"
```

- [ ] **Step 3: Update README**

In `README.md`, in the Subcommand reference section, the existing `darek serve` row already covers it. Add a new top-level section after the "FreshRSS" section (around line 226):

```markdown
## WhatsApp

WhatsApp integration uses the unofficial multi-device protocol via [whatsmeow](https://github.com/tulir/whatsmeow). **This violates WhatsApp's Terms of Service.** A read-only personal-account ingest carries some risk of your number being banned by Meta. You opt in by enabling the feature in config; you can disable it instantly by flipping the flag.

### Configure

```yaml
whatsapp:
  enabled: true
  store_path: ~/.darek/whatsapp/store.db   # whatsmeow session SQLite (auto-created)
```

### Pair

Start `darek serve`, open <http://127.0.0.1:7777/whatsapp>, scan the QR code from your phone (WhatsApp → Settings → Linked devices → Link a device). Once paired, the page swaps to a list of your groups.

### Pick groups

Each group has a checkbox. Toggle the ones you want tracked. Messages start landing in `whatsapp_messages` immediately for opted-in groups. Click *Refresh from WhatsApp* to pick up newly-joined groups. Click *Unpair* to log out and wipe the local session (Postgres data is preserved).

### What's stored

Text messages go in verbatim. Media (images, voice, video, documents, stickers) become short placeholders like `[image]`, `[voice 12s]`, `[document: report.pdf]` — sufficient for future summarization, no media payloads downloaded. Reactions, edits, deletes, and reply-quoted context are dropped. Direct messages are not ingested at all (groups only).

Sub-projects B (`whatsapp.summarize_group` agent tool) and C (`whatsapp.send` agent tool) build on this base; they ship as separate plans.
```

- [ ] **Step 4: Verify**

Run:

```bash
cd /Users/bklimczak/Projects/darek && go build ./... && make test && make lint
```

Expected: clean across the board.

- [ ] **Step 5: Manual smoke (requires a real phone)**

```bash
# 1. apply migration
DAREK_POSTGRES_URL=... ./darek migrate

# 2. set whatsapp.enabled=true and start serve
./darek serve

# 3. open http://127.0.0.1:7777/whatsapp, scan QR with phone
# 4. click "Refresh from WhatsApp", tick a test group's checkbox
# 5. send a message in that group from another participant's phone
# 6. confirm in psql:
psql $DAREK_POSTGRES_URL -c "SELECT sender_name, kind, body, sent_at FROM whatsapp_messages ORDER BY sent_at DESC LIMIT 5;"
```

- [ ] **Step 6: Commit**

```bash
git add README.md obs/metrics.go obs/metrics_test.go tools/whatsapp/manager.go
git commit -m "feat(whatsapp): observability + README"
```
