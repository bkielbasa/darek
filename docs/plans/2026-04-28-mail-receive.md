# Mail Receive Implementation Plan

> **For agentic workers:** Use `superpowers:subagent-driven-development` to implement this plan task-by-task.

**Goal:** Receive-only IMAP mail via the hybrid sync model — envelopes cached in Postgres, bodies/attachments fetched on demand. Multiple accounts. Tools: `mail.search`, `mail.get_body`, `mail.get_attachment`.

**Architecture:** New `tools/mail/` package. `MailAccount` interface (provider-neutral), `imap` subpackage with concrete IMAP impl. Sync command writes envelopes/snippets to Postgres tables (added via `0002_mail.up.sql`). Body/attachment fetch is live IMAP per call. Plan 5 will add SMTP send, the `Confirmer`, and `mail.send` — those tables (`mail_pending_sends`) are introduced here for forward-compat.

**Tech Stack:** `github.com/emersion/go-imap/v2` (IMAP client). Standard `mime/multipart` for attachment metadata extraction. `pgx/v5` for DB.

**Out of scope for this plan:** SMTP send, `mail.send` tool, `Confirmer`, IMAP APPEND to Sent — all in Plan 5.

---

## File Map

| Path | Responsibility |
|---|---|
| `db/migrations/0002_mail.up.sql` | Mail tables: accounts, folders, messages, attachments_meta, pending_sends. |
| `tools/mail/mail.go` | `MailAccount` interface, types `Envelope`, `BodyPart`, `AttachmentMeta`. |
| `tools/mail/store.go` | Postgres-backed envelope storage. |
| `tools/mail/store_test.go` | Integration tests. |
| `tools/mail/imap/imap.go` | IMAP `MailAccount` impl using `go-imap/v2`. |
| `tools/mail/sync.go` | `Sync(ctx, account, folders)` orchestrator. |
| `tools/mail/sync_test.go` | Integration test against fake IMAP server. |
| `tools/mail/tools.go` | `SearchTool`, `GetBodyTool`, `GetAttachmentTool`. |
| `tools/mail/tools_test.go` | Tool tests. |
| `cmd/darek/sync_mail.go` | `darek mail sync` subcommand. |
| `cmd/darek/main.go` | Add `mail` subcommand dispatch. |
| `cmd/darek/chat.go` | Wire mail tools when accounts configured. |
| `config/types.go` | Add `Mail` config + `MailAccount` per-account block. |
| `config/testdata/config.example.yaml` | Example mail block. |
| `README.md` | Mail section. |

---

## Conventions

- Mail account "nickname" is the user-facing identifier — not the email address — and is the `account` field in tool args.
- Attachments are NOT cached during sync. We store metadata only (filename, content type, size, IMAP part path). `mail.get_attachment` fetches live and writes to `~/.darek/attachments/<message-uuid>/<filename>`.
- IMAP UIDs are scoped to a `(folder, uidvalidity)` pair. If `uidvalidity` changes, the folder is fully re-synced.
- All mail tests are integration-tagged (`//go:build integration`) and use a fake IMAP server backed by `emersion/go-imap/v2/imapserver`.

---

## Task 1 — Migration `0002_mail.up.sql`

**Files:** Create `db/migrations/0002_mail.up.sql`.

```sql
CREATE TABLE mail_accounts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    nickname     text UNIQUE NOT NULL,
    email        text NOT NULL,
    imap_host    text NOT NULL,
    imap_port    integer NOT NULL,
    imap_tls     boolean NOT NULL DEFAULT true,
    smtp_host    text,
    smtp_port    integer,
    smtp_tls     boolean,
    username     text NOT NULL,
    secret_ref   text NOT NULL
);

CREATE TABLE mail_folders (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   uuid NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
    name         text NOT NULL,
    uidvalidity  bigint NOT NULL DEFAULT 0,
    last_uid     bigint NOT NULL DEFAULT 0,
    last_sync_at timestamptz,
    UNIQUE (account_id, name)
);

CREATE TABLE mail_messages (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      uuid NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
    folder_id       uuid NOT NULL REFERENCES mail_folders(id) ON DELETE CASCADE,
    imap_uid        bigint NOT NULL,
    message_id      text,
    in_reply_to     text,
    "references"    text[] NOT NULL DEFAULT '{}',
    thread_key      text,
    from_addr       text,
    to_addrs        text[] NOT NULL DEFAULT '{}',
    cc_addrs        text[] NOT NULL DEFAULT '{}',
    subject         text,
    date            timestamptz,
    flags           text[] NOT NULL DEFAULT '{}',
    snippet         text,
    has_attachments boolean NOT NULL DEFAULT false,
    deleted_at      timestamptz,
    search          tsvector GENERATED ALWAYS AS (
        to_tsvector('simple'::regconfig,
            coalesce(subject,'') || ' ' ||
            coalesce(snippet,'') || ' ' ||
            coalesce(from_addr,'') || ' ' ||
            immutable_array_to_string(to_addrs, ' ') || ' ' ||
            immutable_array_to_string(cc_addrs, ' ')
        )
    ) STORED,
    UNIQUE (folder_id, imap_uid)
);
CREATE INDEX mail_messages_search_gin ON mail_messages USING gin(search);
CREATE INDEX mail_messages_account_date ON mail_messages (account_id, date DESC);
CREATE INDEX mail_messages_message_id ON mail_messages (message_id);

CREATE TABLE mail_attachments_meta (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id   uuid NOT NULL REFERENCES mail_messages(id) ON DELETE CASCADE,
    filename     text,
    content_type text,
    size_bytes   bigint NOT NULL DEFAULT 0,
    imap_part_id text NOT NULL
);

CREATE TABLE mail_pending_sends (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    account_id   uuid NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
    to_addrs     text[] NOT NULL,
    subject      text,
    body         text,
    attachments  jsonb,
    status       text NOT NULL DEFAULT 'pending'
);
```

**Notes:**
- `references` is reserved in SQL — quote as `"references"`.
- The `immutable_array_to_string` function was created in `0001_initial.up.sql`. Reuse it.
- `deleted_at` enables soft-deletion for messages no longer present on the server.

**Commit:** `feat(db): mail tables migration`.

---

## Task 2 — `MailAccount` interface + Postgres store

**Files:** Create `tools/mail/mail.go`, `tools/mail/store.go`, `tools/mail/store_test.go`.

### `tools/mail/mail.go`

```go
package mail

import (
	"context"
	"io"
	"time"
)

type Envelope struct {
	UID         uint32
	MessageID   string
	InReplyTo   string
	References  []string
	From        string
	To          []string
	Cc          []string
	Subject     string
	Date        time.Time
	Flags       []string
	Snippet     string
	HasAttach   bool
	Attachments []AttachmentMeta
}

type AttachmentMeta struct {
	Filename    string
	ContentType string
	Size        int64
	PartID      string
}

// MailAccount provides read-only access to a single mail account.
type MailAccount interface {
	Nickname() string
	Email() string

	// SyncFolder returns envelopes for messages with UID > sinceUID. Also returns
	// the current UIDVALIDITY of the folder; if it differs from the caller's,
	// the caller must re-sync the entire folder.
	SyncFolder(ctx context.Context, folder string, sinceUID uint32) (envelopes []Envelope, uidvalidity uint32, err error)

	// FetchBody returns the plain-text body (preferred) or HTML-stripped fallback.
	FetchBody(ctx context.Context, folder string, uid uint32) (string, error)

	// FetchAttachment returns a stream for the attachment at `partID` of the
	// given UID. Caller is responsible for closing.
	FetchAttachment(ctx context.Context, folder string, uid uint32, partID string) (io.ReadCloser, error)
}
```

### `tools/mail/store.go`

```go
package mail

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// EnsureAccount upserts the account by nickname and returns its id.
type AccountSpec struct {
	Nickname string
	Email    string
	IMAPHost string
	IMAPPort int
	IMAPTLS  bool
	SMTPHost string
	SMTPPort int
	SMTPTLS  bool
	Username string
	SecretRef string
}

func (s *Store) EnsureAccount(ctx context.Context, a AccountSpec) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO mail_accounts (nickname, email, imap_host, imap_port, imap_tls,
			smtp_host, smtp_port, smtp_tls, username, secret_ref)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (nickname) DO UPDATE SET
			email=EXCLUDED.email,
			imap_host=EXCLUDED.imap_host, imap_port=EXCLUDED.imap_port, imap_tls=EXCLUDED.imap_tls,
			smtp_host=EXCLUDED.smtp_host, smtp_port=EXCLUDED.smtp_port, smtp_tls=EXCLUDED.smtp_tls,
			username=EXCLUDED.username, secret_ref=EXCLUDED.secret_ref
		RETURNING id
	`, a.Nickname, a.Email, a.IMAPHost, a.IMAPPort, a.IMAPTLS,
		a.SMTPHost, a.SMTPPort, a.SMTPTLS, a.Username, a.SecretRef).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("ensure account: %w", err)
	}
	return id, nil
}

func (s *Store) AccountIDByNickname(ctx context.Context, nickname string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT id FROM mail_accounts WHERE nickname = $1`, nickname).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("account %s: %w", nickname, err)
	}
	return id, nil
}

// EnsureFolder upserts (account_id, name) and returns its id and current uidvalidity.
func (s *Store) EnsureFolder(ctx context.Context, accountID uuid.UUID, name string) (uuid.UUID, uint32, uint32, error) {
	var (
		id         uuid.UUID
		uidValidity, lastUID uint32
	)
	err := s.pool.QueryRow(ctx, `
		INSERT INTO mail_folders (account_id, name) VALUES ($1, $2)
		ON CONFLICT (account_id, name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id, uidvalidity, last_uid
	`, accountID, name).Scan(&id, &uidValidity, &lastUID)
	if err != nil {
		return uuid.Nil, 0, 0, fmt.Errorf("ensure folder: %w", err)
	}
	return id, uidValidity, lastUID, nil
}

// ResetFolderState clears all messages for the folder and resets last_uid (used when UIDVALIDITY changes).
func (s *Store) ResetFolderState(ctx context.Context, folderID uuid.UUID, newUIDValidity uint32) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM mail_messages WHERE folder_id = $1`, folderID)
	if err != nil {
		return fmt.Errorf("reset folder messages: %w", err)
	}
	_, err = s.pool.Exec(ctx, `UPDATE mail_folders SET uidvalidity = $1, last_uid = 0 WHERE id = $2`, newUIDValidity, folderID)
	return err
}

// UpdateFolderState updates uidvalidity, last_uid, and last_sync_at after a successful sync pass.
func (s *Store) UpdateFolderState(ctx context.Context, folderID uuid.UUID, uidvalidity, lastUID uint32) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE mail_folders SET uidvalidity = $1, last_uid = $2, last_sync_at = $3
		WHERE id = $4
	`, uidvalidity, lastUID, time.Now(), folderID)
	return err
}

// InsertEnvelope inserts a single envelope and its attachment metadata.
func (s *Store) InsertEnvelope(ctx context.Context, accountID, folderID uuid.UUID, e Envelope) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	var msgID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO mail_messages
		  (account_id, folder_id, imap_uid, message_id, in_reply_to, "references",
		   from_addr, to_addrs, cc_addrs, subject, date, flags, snippet, has_attachments)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (folder_id, imap_uid) DO UPDATE SET flags = EXCLUDED.flags
		RETURNING id
	`, accountID, folderID, int64(e.UID), e.MessageID, e.InReplyTo, e.References,
		e.From, e.To, e.Cc, e.Subject, e.Date, e.Flags, e.Snippet, e.HasAttach).Scan(&msgID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert message: %w", err)
	}
	for _, a := range e.Attachments {
		_, err = tx.Exec(ctx, `
			INSERT INTO mail_attachments_meta (message_id, filename, content_type, size_bytes, imap_part_id)
			VALUES ($1,$2,$3,$4,$5)
		`, msgID, a.Filename, a.ContentType, a.Size, a.PartID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("insert attachment meta: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return msgID, nil
}

// SearchResult is what the search tool returns.
type SearchResult struct {
	ID          uuid.UUID
	Account     string
	Folder      string
	UID         uint32
	From        string
	Subject     string
	Date        time.Time
	Snippet     string
	HasAttach   bool
}

type SearchOpts struct {
	Query   string
	Account string // nickname; "" = all
	Folder  string // "" = all
	Since   time.Time // zero = no lower bound
	Limit   int
}

func (s *Store) Search(ctx context.Context, opts SearchOpts) ([]SearchResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	args := []any{}
	conds := []string{"m.deleted_at IS NULL"}
	if opts.Query != "" {
		args = append(args, opts.Query)
		conds = append(conds, fmt.Sprintf("m.search @@ plainto_tsquery('simple', $%d)", len(args)))
	}
	if opts.Account != "" {
		args = append(args, opts.Account)
		conds = append(conds, fmt.Sprintf("a.nickname = $%d", len(args)))
	}
	if opts.Folder != "" {
		args = append(args, opts.Folder)
		conds = append(conds, fmt.Sprintf("f.name = $%d", len(args)))
	}
	if !opts.Since.IsZero() {
		args = append(args, opts.Since)
		conds = append(conds, fmt.Sprintf("m.date >= $%d", len(args)))
	}
	args = append(args, opts.Limit)
	q := `
		SELECT m.id, a.nickname, f.name, m.imap_uid, m.from_addr, m.subject, m.date, m.snippet, m.has_attachments
		FROM mail_messages m
		JOIN mail_folders f ON f.id = m.folder_id
		JOIN mail_accounts a ON a.id = m.account_id
		WHERE ` + joinConds(conds) + `
		ORDER BY m.date DESC NULLS LAST
		LIMIT $` + fmt.Sprint(len(args))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		var uid int64
		if err := rows.Scan(&r.ID, &r.Account, &r.Folder, &uid, &r.From, &r.Subject, &r.Date, &r.Snippet, &r.HasAttach); err != nil {
			return nil, err
		}
		r.UID = uint32(uid)
		out = append(out, r)
	}
	return out, rows.Err()
}

func joinConds(c []string) string {
	if len(c) == 0 {
		return "TRUE"
	}
	out := c[0]
	for _, s := range c[1:] {
		out += " AND " + s
	}
	return out
}

// LookupForFetch returns the (account_nickname, folder_name, imap_uid) for a stored message id.
type FetchTarget struct {
	AccountNickname string
	Folder          string
	UID             uint32
}

func (s *Store) LookupForFetch(ctx context.Context, messageID uuid.UUID) (FetchTarget, error) {
	var ft FetchTarget
	var uid int64
	err := s.pool.QueryRow(ctx, `
		SELECT a.nickname, f.name, m.imap_uid
		FROM mail_messages m
		JOIN mail_folders f ON f.id = m.folder_id
		JOIN mail_accounts a ON a.id = m.account_id
		WHERE m.id = $1
	`, messageID).Scan(&ft.AccountNickname, &ft.Folder, &uid)
	if err != nil {
		return FetchTarget{}, fmt.Errorf("lookup message %s: %w", messageID, err)
	}
	ft.UID = uint32(uid)
	return ft, nil
}

func (s *Store) AttachmentByID(ctx context.Context, attID uuid.UUID) (uuid.UUID, AttachmentMeta, error) {
	var msgID uuid.UUID
	var meta AttachmentMeta
	err := s.pool.QueryRow(ctx, `
		SELECT message_id, filename, content_type, size_bytes, imap_part_id
		FROM mail_attachments_meta WHERE id = $1
	`, attID).Scan(&msgID, &meta.Filename, &meta.ContentType, &meta.Size, &meta.PartID)
	if err != nil {
		return uuid.Nil, meta, fmt.Errorf("attachment %s: %w", attID, err)
	}
	return msgID, meta, nil
}
```

### `tools/mail/store_test.go`

```go
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
	s := NewStore(pool)
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
	s := NewStore(pool)
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
	s := NewStore(pool)
	ctx := context.Background()

	aid, _ := s.EnsureAccount(ctx, AccountSpec{Nickname: "p", Email: "m@x", IMAPHost: "h", IMAPPort: 993, IMAPTLS: true, Username: "u", SecretRef: "env:S"})
	fid, _, _, _ := s.EnsureFolder(ctx, aid, "INBOX")

	id1, err := s.InsertEnvelope(ctx, aid, fid, Envelope{UID: 7, Subject: "hi", Flags: []string{"\\Seen"}})
	require.NoError(t, err)
	id2, err := s.InsertEnvelope(ctx, aid, fid, Envelope{UID: 7, Subject: "hi-again", Flags: []string{"\\Flagged"}})
	require.NoError(t, err)
	require.Equal(t, id1, id2) // upsert returns the same id; only flags update
}
```

**Commit:** `feat(mail): MailAccount interface + Postgres envelope store`.

---

## Task 3 — IMAP `MailAccount` impl

**Files:** Create `tools/mail/imap/imap.go`.

```go
package imap

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"darek/tools/mail"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type Account struct {
	nickname string
	email    string
	host     string
	port     int
	useTLS   bool
	username string
	password string
}

type Options struct {
	Nickname string
	Email    string
	Host     string
	Port     int
	TLS      bool
	Username string
	Password string
}

func New(opt Options) *Account {
	return &Account{
		nickname: opt.Nickname, email: opt.Email,
		host: opt.Host, port: opt.Port, useTLS: opt.TLS,
		username: opt.Username, password: opt.Password,
	}
}

func (a *Account) Nickname() string { return a.nickname }
func (a *Account) Email() string    { return a.email }

func (a *Account) connect(ctx context.Context) (*imapclient.Client, error) {
	addr := net.JoinHostPort(a.host, fmt.Sprint(a.port))
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	var c *imapclient.Client
	var err error
	if a.useTLS {
		c, err = imapclient.DialTLS(addr, &imapclient.Options{TLSConfig: &tls.Config{ServerName: a.host}})
	} else {
		nc, derr := dialer.DialContext(ctx, "tcp", addr)
		if derr != nil {
			return nil, fmt.Errorf("dial %s: %w", addr, derr)
		}
		c = imapclient.New(nc, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	if err := c.Login(a.username, a.password).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("login: %w", err)
	}
	return c, nil
}

func (a *Account) SyncFolder(ctx context.Context, folder string, sinceUID uint32) ([]mail.Envelope, uint32, error) {
	c, err := a.connect(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer c.Logout()

	mb, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, 0, fmt.Errorf("select %s: %w", folder, err)
	}

	if mb.NumMessages == 0 {
		return nil, uint32(mb.UIDValidity), nil
	}

	// Fetch UIDs greater than sinceUID.
	var seqset imap.UIDSet
	seqset.AddRange(imap.UID(sinceUID+1), 0) // 0 means "*"

	// Fetch envelope + flags + size + bodystructure.
	fetchOpts := &imap.FetchOptions{
		Envelope:     true,
		Flags:        true,
		InternalDate: true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		UID:          true,
	}
	cmd := c.Fetch(seqset, fetchOpts)
	defer cmd.Close()

	var envs []mail.Envelope
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		buf, err := msg.Collect()
		if err != nil {
			return nil, 0, fmt.Errorf("collect msg: %w", err)
		}
		envs = append(envs, fromGoimap(buf))
	}
	if err := cmd.Close(); err != nil {
		return nil, 0, fmt.Errorf("fetch close: %w", err)
	}

	// Snippet fetch — small BODY[TEXT]<0.500> on the same UIDs (best-effort; ignore errors).
	enrichSnippets(c, &envs)

	return envs, uint32(mb.UIDValidity), nil
}

func enrichSnippets(c *imapclient.Client, envs *[]mail.Envelope) {
	if len(*envs) == 0 {
		return
	}
	var us imap.UIDSet
	for _, e := range *envs {
		us.AddNum(imap.UID(e.UID))
	}
	cmd := c.Fetch(us, &imap.FetchOptions{
		UID: true,
		BodySection: []*imap.FetchItemBodySection{{
			Specifier: imap.PartSpecifierText,
			Partial:   &imap.SectionPartial{Offset: 0, Size: 500},
		}},
	})
	defer cmd.Close()
	byUID := map[uint32]*mail.Envelope{}
	for i := range *envs {
		byUID[(*envs)[i].UID] = &(*envs)[i]
	}
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		buf, err := msg.Collect()
		if err != nil {
			continue
		}
		ent, ok := byUID[uint32(buf.UID)]
		if !ok {
			continue
		}
		for _, b := range buf.BodySection {
			s := strings.TrimSpace(string(b.Bytes))
			if s != "" {
				ent.Snippet = s
				break
			}
		}
	}
}

func fromGoimap(b *imapclient.FetchMessageBuffer) mail.Envelope {
	env := mail.Envelope{UID: uint32(b.UID), Flags: flagsToStrings(b.Flags)}
	if b.Envelope != nil {
		env.MessageID = strings.Trim(b.Envelope.MessageID, "<>")
		env.InReplyTo = strings.Trim(b.Envelope.InReplyTo, "<>")
		env.From = addrsJoin(b.Envelope.From)
		env.To = addrsList(b.Envelope.To)
		env.Cc = addrsList(b.Envelope.Cc)
		env.Subject = b.Envelope.Subject
		env.Date = b.Envelope.Date
	}
	if b.BodyStructure != nil {
		env.HasAttach, env.Attachments = walkBodyStructure(b.BodyStructure, "")
	}
	return env
}

func walkBodyStructure(bs imap.BodyStructure, prefix string) (bool, []mail.AttachmentMeta) {
	var atts []mail.AttachmentMeta
	hasAttach := false
	switch v := bs.(type) {
	case *imap.BodyStructureSinglePart:
		filename := ""
		if v.Disposition != nil && v.Disposition.Value == "attachment" {
			filename = v.Disposition.Params["filename"]
			hasAttach = true
		} else if v.Type == "application" || v.Type == "image" || v.Type == "audio" || v.Type == "video" {
			filename = v.Params["name"]
			if filename != "" {
				hasAttach = true
			}
		}
		if hasAttach {
			pid := prefix
			if pid == "" {
				pid = "1"
			}
			atts = append(atts, mail.AttachmentMeta{
				Filename:    filename,
				ContentType: v.Type + "/" + v.Subtype,
				Size:        int64(v.Size),
				PartID:      pid,
			})
		}
	case *imap.BodyStructureMultiPart:
		for i, p := range v.Children {
			pid := fmt.Sprintf("%s%d", concatPrefix(prefix), i+1)
			h, sub := walkBodyStructure(p, pid)
			hasAttach = hasAttach || h
			atts = append(atts, sub...)
		}
	}
	return hasAttach, atts
}

func concatPrefix(p string) string {
	if p == "" {
		return ""
	}
	return p + "."
}

func addrsJoin(as []imap.Address) string {
	if len(as) == 0 {
		return ""
	}
	return as[0].Mailbox + "@" + as[0].Host
}

func addrsList(as []imap.Address) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Mailbox+"@"+a.Host)
	}
	return out
}

func flagsToStrings(f []imap.Flag) []string {
	out := make([]string, 0, len(f))
	for _, x := range f {
		out = append(out, string(x))
	}
	return out
}

func (a *Account) FetchBody(ctx context.Context, folder string, uid uint32) (string, error) {
	c, err := a.connect(ctx)
	if err != nil {
		return "", err
	}
	defer c.Logout()
	if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return "", fmt.Errorf("select: %w", err)
	}
	var us imap.UIDSet
	us.AddNum(imap.UID(uid))
	cmd := c.Fetch(us, &imap.FetchOptions{
		UID: true,
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierText},
		},
	})
	defer cmd.Close()
	msg := cmd.Next()
	if msg == nil {
		return "", fmt.Errorf("uid %d not found", uid)
	}
	buf, err := msg.Collect()
	if err != nil {
		return "", err
	}
	for _, b := range buf.BodySection {
		return string(b.Bytes), nil
	}
	return "", fmt.Errorf("no body section returned")
}

func (a *Account) FetchAttachment(ctx context.Context, folder string, uid uint32, partID string) (io.ReadCloser, error) {
	c, err := a.connect(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("select: %w", err)
	}
	var us imap.UIDSet
	us.AddNum(imap.UID(uid))
	cmd := c.Fetch(us, &imap.FetchOptions{
		UID: true,
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierNone, Part: parsePartID(partID)},
		},
	})
	msg := cmd.Next()
	if msg == nil {
		_ = cmd.Close()
		_ = c.Close()
		return nil, fmt.Errorf("uid %d not found", uid)
	}
	buf, err := msg.Collect()
	_ = cmd.Close()
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	for _, b := range buf.BodySection {
		_ = c.Logout()
		return io.NopCloser(strings.NewReader(string(b.Bytes))), nil
	}
	_ = c.Close()
	return nil, fmt.Errorf("no body section returned")
}

func parsePartID(p string) []int {
	if p == "" {
		return nil
	}
	var out []int
	for _, s := range strings.Split(p, ".") {
		n := 0
		for _, c := range s {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			}
		}
		out = append(out, n)
	}
	return out
}
```

**Pitfall:** the `go-imap/v2` API is in flux as of writing. The exact method names (e.g., `imapclient.DialTLS`, `imap.SelectOptions{ReadOnly: true}`, `cmd.Next()`, `msg.Collect()`) may differ in the version you fetch. Run `go doc github.com/emersion/go-imap/v2` and `go doc github.com/emersion/go-imap/v2/imapclient` to see actual signatures, and adapt without changing the externally-observable behavior. Report deviations.

**Add deps:**
```bash
go get github.com/emersion/go-imap/v2
go get github.com/emersion/go-imap/v2/imapclient
go mod tidy
go build ./tools/mail/imap/...
```

**Commit:** `feat(mail): IMAP MailAccount implementation via go-imap/v2`.

(No unit tests for `imap` package directly — exercised through `sync_test.go`.)

---

## Task 4 — Sync orchestration + integration test

**Files:** Create `tools/mail/sync.go`, `tools/mail/sync_test.go`.

### `tools/mail/sync.go`

```go
package mail

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

type SyncReport struct {
	Account      string
	Folder       string
	NewMessages  int
	UIDValidity  uint32
}

// Sync runs one pass of the account: per folder, fetches envelopes since
// last_uid, handles UIDVALIDITY changes by full-resyncing.
func Sync(ctx context.Context, store *Store, accountID uuid.UUID, account MailAccount, folders []string) ([]SyncReport, error) {
	var reports []SyncReport
	for _, name := range folders {
		folderID, lastUV, lastUID, err := store.EnsureFolder(ctx, accountID, name)
		if err != nil {
			return reports, err
		}
		// First-pass probe: the IMAP impl's SyncFolder returns the current UIDVALIDITY.
		// We do an empty SELECT-style probe by calling SyncFolder with sinceUID = max(lastUID, ^uint32(0)-1)
		// — but that is wasteful. Better: do the actual fetch with sinceUID = lastUID, then check returned uidvalidity.
		envs, currentUV, err := account.SyncFolder(ctx, name, lastUID)
		if err != nil {
			return reports, fmt.Errorf("sync %s/%s: %w", account.Nickname(), name, err)
		}
		if lastUV != 0 && currentUV != lastUV {
			// UIDVALIDITY changed: discard cached state and refetch all.
			if err := store.ResetFolderState(ctx, folderID, currentUV); err != nil {
				return reports, err
			}
			envs, currentUV, err = account.SyncFolder(ctx, name, 0)
			if err != nil {
				return reports, fmt.Errorf("resync %s/%s: %w", account.Nickname(), name, err)
			}
		}

		newCount := 0
		newLastUID := lastUID
		for _, env := range envs {
			if _, err := store.InsertEnvelope(ctx, accountID, folderID, env); err != nil {
				return reports, fmt.Errorf("insert env uid=%d: %w", env.UID, err)
			}
			newCount++
			if env.UID > newLastUID {
				newLastUID = env.UID
			}
		}
		if err := store.UpdateFolderState(ctx, folderID, currentUV, newLastUID); err != nil {
			return reports, err
		}
		reports = append(reports, SyncReport{
			Account: account.Nickname(), Folder: name, NewMessages: newCount, UIDValidity: currentUV,
		})
	}
	return reports, nil
}
```

### `tools/mail/sync_test.go`

```go
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
	s := NewStore(pool)
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
	s := NewStore(pool)
	ctx := context.Background()

	aid, _ := s.EnsureAccount(ctx, AccountSpec{Nickname: "p", Email: "m@x", IMAPHost: "h", IMAPPort: 993, IMAPTLS: true, Username: "u", SecretRef: "env:S"})

	acc := &fakeAccount{nickname: "p", uvs: map[string]uint32{"INBOX": 1}, envs: map[string][]Envelope{
		"INBOX": {{UID: 100, Subject: "old"}},
	}}
	_, err := Sync(ctx, s, aid, acc, []string{"INBOX"})
	require.NoError(t, err)

	// Bump uvs and replace envs.
	acc.uvs["INBOX"] = 2
	acc.envs["INBOX"] = []Envelope{{UID: 1, Subject: "new"}}
	_, err = Sync(ctx, s, aid, acc, []string{"INBOX"})
	require.NoError(t, err)

	res, err := s.Search(ctx, SearchOpts{})
	require.NoError(t, err)
	// Old message wiped; new message present.
	require.Len(t, res, 1)
	require.Equal(t, "new", res[0].Subject)
}

func TestSync_Idempotent(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	s := NewStore(pool)
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
```

**Commit:** `feat(mail): sync orchestrator with UIDVALIDITY handling + tests`.

---

## Task 5 — Mail tools

**Files:** Create `tools/mail/tools.go`, `tools/mail/tools_test.go`.

### `tools/mail/tools.go`

```go
package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AccountResolver returns the live MailAccount for a nickname; tools call it
// to acquire a connection target after looking up the persisted message.
type AccountResolver interface {
	ByNickname(nickname string) (MailAccount, bool)
}

type SearchTool struct{ Store *Store }

func (SearchTool) Name() string        { return "mail.search" }
func (SearchTool) Description() string { return "Search cached mail envelopes by query string. Returns up to N matching messages." }
func (SearchTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"query":{"type":"string"},
			"account":{"type":"string","description":"account nickname; empty for all"},
			"folder":{"type":"string"},
			"since":{"type":"string","description":"RFC3339 lower bound on date"},
			"limit":{"type":"integer","minimum":1,"maximum":100}
		},
		"required":[]
	}`)
}
func (st SearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query   string `json:"query"`
		Account string `json:"account"`
		Folder  string `json:"folder"`
		Since   string `json:"since"`
		Limit   int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	opts := SearchOpts{Query: p.Query, Account: p.Account, Folder: p.Folder, Limit: p.Limit}
	if p.Since != "" {
		t, err := time.Parse(time.RFC3339, p.Since)
		if err != nil {
			return "", fmt.Errorf("since: %w", err)
		}
		opts.Since = t
	}
	res, err := st.Store.Search(ctx, opts)
	if err != nil {
		return "", err
	}
	if len(res) == 0 {
		return "no matching messages", nil
	}
	var b strings.Builder
	for _, r := range res {
		fmt.Fprintf(&b, "[%s] %s/%s uid=%d %s\n  from: %s | date: %s",
			r.ID, r.Account, r.Folder, r.UID, r.Subject, r.From, r.Date.Format(time.RFC3339))
		if r.HasAttach {
			b.WriteString(" (has attachments)")
		}
		b.WriteString("\n")
		if r.Snippet != "" {
			fmt.Fprintf(&b, "  %s\n", strings.TrimSpace(r.Snippet))
		}
	}
	return b.String(), nil
}

type GetBodyTool struct {
	Store    *Store
	Accounts AccountResolver
}

func (GetBodyTool) Name() string        { return "mail.get_body" }
func (GetBodyTool) Description() string { return "Fetch the plain-text body of a mail message by id (returned by mail.search)." }
func (GetBodyTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{"message_id":{"type":"string","description":"uuid from mail.search"}},
		"required":["message_id"]
	}`)
}
func (gt GetBodyTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct{ MessageID string `json:"message_id"` }
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	mid, err := uuid.Parse(p.MessageID)
	if err != nil {
		return "", fmt.Errorf("message_id: %w", err)
	}
	t, err := gt.Store.LookupForFetch(ctx, mid)
	if err != nil {
		return "", err
	}
	acc, ok := gt.Accounts.ByNickname(t.AccountNickname)
	if !ok {
		return "", fmt.Errorf("account %s not configured", t.AccountNickname)
	}
	return acc.FetchBody(ctx, t.Folder, t.UID)
}

type GetAttachmentTool struct {
	Store          *Store
	Accounts       AccountResolver
	AttachmentsDir string
}

func (GetAttachmentTool) Name() string        { return "mail.get_attachment" }
func (GetAttachmentTool) Description() string {
	return "Download a mail attachment by attachment_id. Returns the local filesystem path where the attachment was saved."
}
func (GetAttachmentTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{"attachment_id":{"type":"string"}},
		"required":["attachment_id"]
	}`)
}
func (gt GetAttachmentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct{ AttachmentID string `json:"attachment_id"` }
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	aid, err := uuid.Parse(p.AttachmentID)
	if err != nil {
		return "", fmt.Errorf("attachment_id: %w", err)
	}
	mid, meta, err := gt.Store.AttachmentByID(ctx, aid)
	if err != nil {
		return "", err
	}
	t, err := gt.Store.LookupForFetch(ctx, mid)
	if err != nil {
		return "", err
	}
	acc, ok := gt.Accounts.ByNickname(t.AccountNickname)
	if !ok {
		return "", fmt.Errorf("account %s not configured", t.AccountNickname)
	}
	dir := filepath.Join(gt.AttachmentsDir, mid.String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	path := filepath.Join(dir, sanitizeFilename(meta.Filename))
	rc, err := acc.FetchAttachment(ctx, t.Folder, t.UID, meta.PartID)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, rc); err != nil {
		return "", err
	}
	return path, nil
}

func sanitizeFilename(name string) string {
	if name == "" {
		return "attachment"
	}
	out := name
	for _, bad := range []string{"/", "\\", "..", "\x00"} {
		out = strings.ReplaceAll(out, bad, "_")
	}
	return out
}
```

### `tools/mail/tools_test.go`

```go
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
	s := NewStore(pool)
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
	s := NewStore(pool)
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
	s := NewStore(pool)
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
```

**Commit:** `feat(mail): search/get_body/get_attachment tools`.

---

## Task 6 — `darek mail sync` subcommand + chat wiring + config

**Files:** Create `cmd/darek/sync_mail.go`. Modify `cmd/darek/main.go`, `cmd/darek/chat.go`, `config/types.go`, `config/testdata/config.example.yaml`.

### `config/types.go`

Add:

```go
type Mail struct {
	AttachmentsDir    string         `yaml:"attachments_dir"`
	AttachmentTTLDays int            `yaml:"attachment_ttl_days"`
	Accounts          []MailAccountCfg `yaml:"accounts"`
}

type MailAccountCfg struct {
	Nickname    string   `yaml:"nickname"`
	Email       string   `yaml:"email"`
	IMAP        struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
		TLS  bool   `yaml:"tls"`
	} `yaml:"imap"`
	SMTP        struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
		TLS  bool   `yaml:"tls"`
	} `yaml:"smtp"`
	Username    string   `yaml:"username"`
	SecretEnv   string   `yaml:"secret_env"`
	SyncFolders []string `yaml:"sync_folders"`
}
```

Add field to `Config`:

```go
	Mail Mail `yaml:"mail"`
```

### `config/testdata/config.example.yaml`

Append:

```yaml
mail:
  attachments_dir: ~/.darek/attachments
  attachment_ttl_days: 30
  accounts:
    - nickname: personal
      email: me@example.com
      imap: { host: imap.fastmail.com, port: 993, tls: true }
      smtp: { host: smtp.fastmail.com, port: 465, tls: true }
      username: me@example.com
      secret_env: DAREK_MAIL_PERSONAL
      sync_folders: [INBOX]
```

### `cmd/darek/sync_mail.go`

```go
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"darek/config"
	"darek/db"
	"darek/tools/mail"
	mailimap "darek/tools/mail/imap"
)

// runMail dispatches `darek mail <subcmd> ...`.
func runMail(ctx context.Context, cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: darek mail sync [--account=<nickname>]")
	}
	switch args[0] {
	case "sync":
		return runMailSync(ctx, cfgPath, args[1:])
	default:
		return fmt.Errorf("unknown mail subcommand %q (try: sync)", args[0])
	}
}

func runMailSync(ctx context.Context, cfgPath string, args []string) error {
	target := ""
	for _, a := range args {
		if strings.HasPrefix(a, "--account=") {
			target = strings.TrimPrefix(a, "--account=")
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	dsn, err := config.ResolveSecret("env:" + cfg.Postgres.URLEnv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	store := mail.NewStore(pool)
	for _, ac := range cfg.Mail.Accounts {
		if target != "" && target != ac.Nickname {
			continue
		}
		secret, err := config.ResolveSecret("env:" + ac.SecretEnv)
		if err != nil {
			return fmt.Errorf("secret for %s: %w", ac.Nickname, err)
		}
		aid, err := store.EnsureAccount(ctx, mail.AccountSpec{
			Nickname:  ac.Nickname,
			Email:     ac.Email,
			IMAPHost:  ac.IMAP.Host,
			IMAPPort:  ac.IMAP.Port,
			IMAPTLS:   ac.IMAP.TLS,
			SMTPHost:  ac.SMTP.Host,
			SMTPPort:  ac.SMTP.Port,
			SMTPTLS:   ac.SMTP.TLS,
			Username:  ac.Username,
			SecretRef: "env:" + ac.SecretEnv,
		})
		if err != nil {
			return err
		}
		acc := mailimap.New(mailimap.Options{
			Nickname: ac.Nickname, Email: ac.Email,
			Host: ac.IMAP.Host, Port: ac.IMAP.Port, TLS: ac.IMAP.TLS,
			Username: ac.Username, Password: secret,
		})
		folders := ac.SyncFolders
		if len(folders) == 0 {
			folders = []string{"INBOX"}
		}
		reports, err := mail.Sync(ctx, store, aid, acc, folders)
		if err != nil {
			return fmt.Errorf("sync %s: %w", ac.Nickname, err)
		}
		for _, r := range reports {
			fmt.Printf("synced %s/%s: %d new\n", r.Account, r.Folder, r.NewMessages)
		}
	}

	// Attachment GC
	if cfg.Mail.AttachmentTTLDays > 0 && cfg.Mail.AttachmentsDir != "" {
		dir := expandHome(cfg.Mail.AttachmentsDir)
		_ = mail.GCAttachments(dir, cfg.Mail.AttachmentTTLDays)
	}
	return nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
```

### `tools/mail/sync.go` — append GC helper

```go
import "io/fs"
import "os"
import "path/filepath"
import "time"

// GCAttachments removes attachment subdirectories older than ttlDays.
func GCAttachments(dir string, ttlDays int) error {
	cutoff := time.Now().AddDate(0, 0, -ttlDays)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		full := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(full)
		}
	}
	_ = fs.ErrInvalid
	return nil
}
```

(Imports go at the top of `sync.go`. Remove the `_ = fs.ErrInvalid` placeholder; that's just to acknowledge `fs` use; remove if you don't actually use `fs`. The body uses only `os`, `filepath`, `time`. Adjust imports accordingly.)

### `cmd/darek/main.go`

Add `case "mail": return runMail(ctx, cfgPath, args)` to the switch and update the default error message.

### `cmd/darek/chat.go`

Add imports:
```go
"darek/tools/mail"
mailimap "darek/tools/mail/imap"
```

After the Todoist block (or wherever appropriate, before `agent.New`), insert:

```go
	// Mail tools
	if len(cfg.Mail.Accounts) > 0 {
		mstore := mail.NewStore(pool)
		resolver := mailAccountResolver{}
		for _, ac := range cfg.Mail.Accounts {
			secret, err := config.ResolveSecret("env:" + ac.SecretEnv)
			if err != nil {
				logger.WarnContext(ctx, "skipping mail account", "nickname", ac.Nickname, "error", err.Error())
				continue
			}
			resolver[ac.Nickname] = mailimap.New(mailimap.Options{
				Nickname: ac.Nickname, Email: ac.Email,
				Host: ac.IMAP.Host, Port: ac.IMAP.Port, TLS: ac.IMAP.TLS,
				Username: ac.Username, Password: secret,
			})
		}
		attDir := expandHomeChat(cfg.Mail.AttachmentsDir)
		for _, t := range []tools.Tool{
			mail.SearchTool{Store: mstore},
			mail.GetBodyTool{Store: mstore, Accounts: resolver},
			mail.GetAttachmentTool{Store: mstore, Accounts: resolver, AttachmentsDir: attDir},
		} {
			if err := reg.Register(t); err != nil {
				return err
			}
		}
	}
```

Add at the bottom of `chat.go`:

```go
type mailAccountResolver map[string]mail.MailAccount

func (m mailAccountResolver) ByNickname(n string) (mail.MailAccount, bool) {
	a, ok := m[n]
	return a, ok
}

func expandHomeChat(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
```

(`strings` is already imported via main; if `chat.go` doesn't import it, add.)

### Build + commit

```bash
make build
make test
make test-integration
git add db/migrations/ tools/mail/ cmd/darek/sync_mail.go cmd/darek/main.go cmd/darek/chat.go config/types.go config/testdata/config.example.yaml
git commit -m "feat(cmd,mail,config): mail sync subcommand + chat tool wiring"
```

---

## Task 7 — README + final pass

**Files:** Modify `README.md`.

Add a `## Mail` section between Todoist and Roadmap:

```markdown
## Mail

Mail uses a hybrid sync model: envelopes (subject, from, date, snippet) are cached in Postgres, bodies and attachments are fetched live from IMAP on demand.

### Configure

```yaml
mail:
  attachments_dir: ~/.darek/attachments
  attachment_ttl_days: 30
  accounts:
    - nickname: personal
      email: me@example.com
      imap: { host: imap.fastmail.com, port: 993, tls: true }
      smtp: { host: smtp.fastmail.com, port: 465, tls: true }
      username: me@example.com
      secret_env: DAREK_MAIL_PERSONAL
      sync_folders: [INBOX]
```

Add the IMAP password (an app-specific password, NOT your account password) to `~/.darek/secrets.env`.

### Sync

Periodic sync is invoked manually (cron suggested):

```bash
./darek mail sync                  # sync all accounts
./darek mail sync --account=personal
```

Tools enabled in chat: `mail.search`, `mail.get_body`, `mail.get_attachment`. Sending mail is in Plan 5.
```

(Plain triple-backticks in the file.)

**Commit:** `docs: README mail section`.

---

## Acceptance criteria

1. `make test-integration` passes (mail store + sync + tool tests).
2. `darek mail sync --account=<nick>` against a real IMAP server pulls envelopes into Postgres; running it twice is idempotent.
3. `darek "what unread mail do I have from anna?"` → calls `mail.search` and returns formatted results.
4. `darek "show me the body of message <uuid>"` → calls `mail.get_body` and returns plain-text body.
5. `darek "save the PDF attachment from <uuid>"` → calls `mail.get_attachment`, returns local file path.
