package mail

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// Sender is the SMTP-side dependency the tool needs.
type Sender interface {
	Send(ctx context.Context, from string, recipients []string, raw []byte) error
}

// Appender is the IMAP-side dependency for writing to Sent. May be nil to skip.
type Appender interface {
	Append(ctx context.Context, folder string, flags []string, raw []byte) error
}

// SendDeps wires together the per-account capabilities for sending.
type SendDeps struct {
	From       string   // sender mailbox (e.g., me@example.com)
	SMTP       Sender
	Appender   Appender // optional; pass nil to skip APPEND
	SentFolder string   // defaults to "Sent"
	Hostname   string   // for Message-ID
}

// AccountSendResolver resolves send dependencies by account nickname.
type AccountSendResolver interface {
	SendDepsFor(nickname string) (SendDeps, bool)
}

// SendTool implements the mail.send tool.
type SendTool struct {
	Store    *Store
	Accounts AccountSendResolver
	Confirm  Confirmer
}

func (SendTool) Name() string { return "mail.send" }
func (SendTool) Description() string {
	return "Send an email via the named account. Confirms with the user before sending. " +
		"For replies, pass the darek message id of the original mail in `in_reply_to`."
}
func (SendTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"account":{"type":"string","description":"sending account nickname"},
			"to":{"type":"array","items":{"type":"string"}},
			"cc":{"type":"array","items":{"type":"string"}},
			"bcc":{"type":"array","items":{"type":"string"}},
			"subject":{"type":"string"},
			"body":{"type":"string"},
			"in_reply_to":{"type":"string","description":"darek uuid of message being replied to"},
			"attachments":{"type":"array","items":{"type":"string","description":"local file paths"}}
		},
		"required":["account","to","body"]
	}`)
}

func (st SendTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Account     string   `json:"account"`
		To          []string `json:"to"`
		Cc          []string `json:"cc"`
		Bcc         []string `json:"bcc"`
		Subject     string   `json:"subject"`
		Body        string   `json:"body"`
		InReplyTo   string   `json:"in_reply_to"`
		Attachments []string `json:"attachments"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if p.Account == "" || len(p.To) == 0 || p.Body == "" {
		return "", fmt.Errorf("account, to, and body required")
	}

	deps, ok := st.Accounts.SendDepsFor(p.Account)
	if !ok {
		return "", fmt.Errorf("account %s not configured for sending", p.Account)
	}
	if deps.SMTP == nil {
		return "", fmt.Errorf("account %s has no SMTP configured", p.Account)
	}
	sentFolder := deps.SentFolder
	if sentFolder == "" {
		sentFolder = "Sent"
	}

	// Resolve threading from the darek message uuid.
	var inReplyMsgID string
	var refs []string
	subject := p.Subject
	if p.InReplyTo != "" {
		mid, err := uuid.Parse(p.InReplyTo)
		if err != nil {
			return "", fmt.Errorf("in_reply_to: %w", err)
		}
		ctx2, cancel := context.WithCancel(ctx)
		defer cancel()
		ref, err := st.Store.LookupMessageRef(ctx2, mid)
		if err != nil {
			return "", err
		}
		inReplyMsgID = ref.MessageID
		refs = append([]string{}, ref.References...)
		if inReplyMsgID != "" {
			refs = append(refs, inReplyMsgID)
		}
		if ref.Subject != "" && p.Subject == "" {
			subject = "Re: " + ref.Subject
		}
	}

	built, err := BuildMessage(BuildInput{
		From: deps.From, To: p.To, Cc: p.Cc, Bcc: p.Bcc,
		Subject: subject, Body: p.Body,
		InReplyTo: inReplyMsgID, References: refs,
		Attachments: p.Attachments, Hostname: deps.Hostname,
	})
	if err != nil {
		return "", err
	}

	preview := Preview{
		Account: p.Account, From: deps.From,
		To: p.To, Cc: p.Cc, Bcc: p.Bcc,
		Subject: subject, Body: p.Body, Attachments: p.Attachments,
	}
	ok, err = st.Confirm.Confirm(ctx, preview)
	if err != nil {
		return "", fmt.Errorf("confirm: %w", err)
	}
	if !ok {
		return "user declined to send", nil
	}

	allRcpts := append([]string{}, p.To...)
	allRcpts = append(allRcpts, p.Cc...)
	allRcpts = append(allRcpts, p.Bcc...)
	if err := deps.SMTP.Send(ctx, deps.From, allRcpts, built.Bytes); err != nil {
		return "", fmt.Errorf("smtp send: %w", err)
	}

	if deps.Appender != nil {
		if err := deps.Appender.Append(ctx, sentFolder, []string{`\Seen`}, built.Bytes); err != nil {
			// Don't fail the tool on Sent-folder write — the message is already delivered.
			return fmt.Sprintf("sent (message-id %s); WARNING: failed to APPEND to %s: %v",
				built.MessageID, sentFolder, err), nil
		}
	}
	return fmt.Sprintf("sent (message-id %s)", built.MessageID), nil
}
