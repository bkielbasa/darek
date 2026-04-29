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
