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
	Nickname  string
	Email     string
	IMAPHost  string
	IMAPPort  int
	IMAPTLS   bool
	SMTPHost  string
	SMTPPort  int
	SMTPTLS   bool
	Username  string
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
		id                   uuid.UUID
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
	// Normalize nil slices to empty slices so pgx sends '{}' rather than NULL.
	if e.References == nil {
		e.References = []string{}
	}
	if e.To == nil {
		e.To = []string{}
	}
	if e.Cc == nil {
		e.Cc = []string{}
	}
	if e.Flags == nil {
		e.Flags = []string{}
	}

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
	ID        uuid.UUID
	Account   string
	Folder    string
	UID       uint32
	From      string
	Subject   string
	Date      time.Time
	Snippet   string
	HasAttach bool
}

type SearchOpts struct {
	Query   string
	Account string    // nickname; "" = all
	Folder  string    // "" = all
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

type MessageRef struct {
	MessageID  string
	Subject    string
	References []string
}

func (s *Store) LookupMessageRef(ctx context.Context, id uuid.UUID) (MessageRef, error) {
	var r MessageRef
	err := s.pool.QueryRow(ctx, `
		SELECT message_id, subject, "references"
		FROM mail_messages WHERE id = $1
	`, id).Scan(&r.MessageID, &r.Subject, &r.References)
	if err != nil {
		return MessageRef{}, fmt.Errorf("lookup ref %s: %w", id, err)
	}
	return r, nil
}
