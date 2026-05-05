package whatsapp

import (
	"context"
	"errors"
	"time"

	"darek/db"

	"github.com/jackc/pgx/v5"
)

type Store struct {
	pool *db.Pool
}

func NewStore(pool *db.Pool) *Store {
	return &Store{pool: pool}
}

// Group is the row shape returned by Groups(); MessageCount and LastMessageAt
// are derived from whatsapp_messages, not stored on the row itself.
type Group struct {
	JID           string
	Name          string
	IngestEnabled bool
	MessageCount  int
	LastMessageAt *time.Time
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
	_, err := s.pool.Exec(ctx, `
		INSERT INTO whatsapp_groups (jid, name)
		VALUES ($1, $2)
		ON CONFLICT (jid) DO UPDATE
		   SET name           = EXCLUDED.name,
		       last_synced_at = now()
	`, jid, name)
	return err
}

// SetIngestEnabled flips the flag on a single group.
func (s *Store) SetIngestEnabled(ctx context.Context, jid string, enabled bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE whatsapp_groups SET ingest_enabled = $2 WHERE jid = $1`,
		jid, enabled)
	return err
}

// IngestEnabled reports whether the group exists and has the flag set.
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

// InsertMessage inserts a row; ON CONFLICT (id) DO NOTHING keeps it idempotent.
func (s *Store) InsertMessage(ctx context.Context, m Message) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO whatsapp_messages (id, group_jid, sender_jid, sender_name, kind, body, sent_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO NOTHING
	`, m.ID, m.GroupJID, m.SenderJID, m.SenderName, m.Kind, m.Body, m.SentAt)
	return err
}

// Groups returns every row from whatsapp_groups joined with per-group counts.
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

// OptedInGroups returns groups where ingest_enabled = true, ordered by name.
// Used by BuildSummary as the outer loop.
func (s *Store) OptedInGroups(ctx context.Context) ([]Group, error) {
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
		 WHERE g.ingest_enabled = true
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

// UnsummarizedMessages returns messages for the group where summarized_at IS
// NULL and sent_at >= now() - lookbackDays. Sorted ascending by sent_at.
func (s *Store) UnsummarizedMessages(ctx context.Context, groupJID string, lookbackDays int) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, group_jid, sender_jid, sender_name, kind, body, sent_at
		  FROM whatsapp_messages
		 WHERE group_jid = $1
		   AND summarized_at IS NULL
		   AND sent_at >= now() - make_interval(days => $2)
		 ORDER BY sent_at
	`, groupJID, lookbackDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.GroupJID, &m.SenderJID, &m.SenderName, &m.Kind, &m.Body, &m.SentAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkSummarized sets summarized_at = now() for the given message IDs in one
// statement. Idempotent: messages already summarized or missing IDs are
// silently skipped (the WHERE clause matches nothing for them).
func (s *Store) MarkSummarized(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE whatsapp_messages SET summarized_at = now() WHERE id = ANY($1)`,
		ids)
	return err
}
