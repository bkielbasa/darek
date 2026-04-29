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
