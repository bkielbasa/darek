package imap

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"darek/obs"
	"darek/tools/mail"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Compile-time interface satisfaction check.
var _ mail.MailAccount = (*Account)(nil)

// Account is an IMAP-backed MailAccount implementation.
type Account struct {
	nickname string
	email    string
	host     string
	port     int
	useTLS   bool
	username string
	password string
}

// Options holds constructor parameters for New.
type Options struct {
	Nickname string
	Email    string
	Host     string
	Port     int
	TLS      bool
	Username string
	Password string
}

// New creates a new Account from Options.
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
		// DialTLS(addr, *imapclient.Options) — TLS config goes inside Options.
		c, err = imapclient.DialTLS(addr, &imapclient.Options{
			TLSConfig: &tls.Config{ServerName: a.host},
		})
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

// SyncFolder returns envelopes for messages with UID > sinceUID and the
// current UIDVALIDITY for the folder.
func (a *Account) SyncFolder(ctx context.Context, folder string, sinceUID uint32) ([]mail.Envelope, uint32, error) {
	var envs []mail.Envelope
	var uidValidity uint32
	depErr := obs.Dep(ctx, "imap", "sync_folder", func(ctx context.Context) error {
		c, err := a.connect(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = c.Logout().Wait() }()

		mb, err := c.Select(folder, &goimap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return fmt.Errorf("select %s: %w", folder, err)
		}
		uidValidity = mb.UIDValidity
		if mb.NumMessages == 0 {
			return nil
		}

		var seqset goimap.UIDSet
		seqset.AddRange(goimap.UID(sinceUID+1), 0)
		fetchOpts := &goimap.FetchOptions{
			Envelope:      true,
			Flags:         true,
			InternalDate:  true,
			BodyStructure: &goimap.FetchItemBodyStructure{Extended: true},
			UID:           true,
		}
		cmd := c.Fetch(seqset, fetchOpts)
		defer cmd.Close()

		for {
			msg := cmd.Next()
			if msg == nil {
				break
			}
			buf, err := msg.Collect()
			if err != nil {
				return fmt.Errorf("collect msg: %w", err)
			}
			envs = append(envs, fromGoimap(buf))
		}
		if err := cmd.Close(); err != nil {
			return fmt.Errorf("fetch close: %w", err)
		}
		enrichSnippets(c, &envs)
		return nil
	})
	if depErr != nil {
		return nil, 0, depErr
	}
	if m, _ := obs.MetricsInstance(); m != nil {
		m.MailEnvelopesSynced.Add(ctx, int64(len(envs)),
			metric.WithAttributes(attribute.String("account", a.nickname)))
	}
	return envs, uidValidity, nil
}

func enrichSnippets(c *imapclient.Client, envs *[]mail.Envelope) {
	if len(*envs) == 0 {
		return
	}
	var us goimap.UIDSet
	for _, e := range *envs {
		us.AddNum(goimap.UID(e.UID))
	}
	snippetSection := &goimap.FetchItemBodySection{
		Specifier: goimap.PartSpecifierText,
		Partial:   &goimap.SectionPartial{Offset: 0, Size: 500},
	}
	cmd := c.Fetch(us, &goimap.FetchOptions{
		UID:         true,
		BodySection: []*goimap.FetchItemBodySection{snippetSection},
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
		// FetchMessageBuffer.BodySection is []FetchBodySectionBuffer, each has .Bytes.
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
		env.MessageID = b.Envelope.MessageID
		// InReplyTo is []string in go-imap/v2 (not a single string).
		if len(b.Envelope.InReplyTo) > 0 {
			env.InReplyTo = b.Envelope.InReplyTo[0]
		}
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

func walkBodyStructure(bs goimap.BodyStructure, prefix string) (bool, []mail.AttachmentMeta) {
	var atts []mail.AttachmentMeta
	hasAttach := false
	switch v := bs.(type) {
	case *goimap.BodyStructureSinglePart:
		filename := ""
		// Disposition() is a method on BodyStructureSinglePart (uses Extended field internally).
		if d := v.Disposition(); d != nil && strings.EqualFold(d.Value, "attachment") {
			filename = d.Params["filename"]
			hasAttach = true
		} else {
			t := strings.ToLower(v.Type)
			if t == "application" || t == "image" || t == "audio" || t == "video" {
				filename = v.Params["name"]
				if filename != "" {
					hasAttach = true
				}
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
	case *goimap.BodyStructureMultiPart:
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

func addrsJoin(as []goimap.Address) string {
	if len(as) == 0 {
		return ""
	}
	return as[0].Mailbox + "@" + as[0].Host
}

func addrsList(as []goimap.Address) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Mailbox+"@"+a.Host)
	}
	return out
}

func flagsToStrings(f []goimap.Flag) []string {
	out := make([]string, 0, len(f))
	for _, x := range f {
		out = append(out, string(x))
	}
	return out
}

// FetchBody fetches and returns the plain-text body of the message with the
// given UID in the given folder.
func (a *Account) FetchBody(ctx context.Context, folder string, uid uint32) (string, error) {
	var body string
	depErr := obs.Dep(ctx, "imap", "fetch_body", func(ctx context.Context) error {
		c, err := a.connect(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = c.Logout().Wait() }()
		if _, err := c.Select(folder, &goimap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("select: %w", err)
		}
		var us goimap.UIDSet
		us.AddNum(goimap.UID(uid))
		textSection := &goimap.FetchItemBodySection{Specifier: goimap.PartSpecifierText}
		cmd := c.Fetch(us, &goimap.FetchOptions{
			UID:         true,
			BodySection: []*goimap.FetchItemBodySection{textSection},
		})
		defer cmd.Close()
		msg := cmd.Next()
		if msg == nil {
			return fmt.Errorf("uid %d not found", uid)
		}
		buf, err := msg.Collect()
		if err != nil {
			return err
		}
		for _, b := range buf.BodySection {
			body = string(b.Bytes)
			return nil
		}
		return fmt.Errorf("no body section returned")
	})
	if depErr != nil {
		return "", depErr
	}
	if m, _ := obs.MetricsInstance(); m != nil {
		m.MailBodiesFetched.Add(ctx, 1, metric.WithAttributes(attribute.String("account", a.nickname)))
	}
	return body, nil
}

// FetchAttachment returns an io.ReadCloser for the attachment at partID of the
// given UID in the given folder. The caller is responsible for closing.
func (a *Account) FetchAttachment(ctx context.Context, folder string, uid uint32, partID string) (io.ReadCloser, error) {
	var rc io.ReadCloser
	depErr := obs.Dep(ctx, "imap", "fetch_attachment", func(ctx context.Context) error {
		c, err := a.connect(ctx)
		if err != nil {
			return err
		}
		if _, err := c.Select(folder, &goimap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			_ = c.Close()
			return fmt.Errorf("select: %w", err)
		}
		var us goimap.UIDSet
		us.AddNum(goimap.UID(uid))
		partSection := &goimap.FetchItemBodySection{
			Specifier: goimap.PartSpecifierNone,
			Part:      parsePartID(partID),
		}
		cmd := c.Fetch(us, &goimap.FetchOptions{
			UID:         true,
			BodySection: []*goimap.FetchItemBodySection{partSection},
		})
		msg := cmd.Next()
		if msg == nil {
			_ = cmd.Close()
			_ = c.Close()
			return fmt.Errorf("uid %d not found", uid)
		}
		buf, err := msg.Collect()
		_ = cmd.Close()
		if err != nil {
			_ = c.Close()
			return err
		}
		for _, b := range buf.BodySection {
			_ = c.Logout().Wait()
			rc = io.NopCloser(strings.NewReader(string(b.Bytes)))
			return nil
		}
		_ = c.Close()
		return fmt.Errorf("no body section returned")
	})
	if depErr != nil {
		return nil, depErr
	}
	if m, _ := obs.MetricsInstance(); m != nil {
		m.MailAttachmentsFetched.Add(ctx, 1, metric.WithAttributes(attribute.String("account", a.nickname)))
	}
	return rc, nil
}

func parsePartID(p string) []int {
	if p == "" {
		return nil
	}
	var out []int
	for _, s := range strings.Split(p, ".") {
		n := 0
		for _, ch := range s {
			if ch >= '0' && ch <= '9' {
				n = n*10 + int(ch-'0')
			}
		}
		out = append(out, n)
	}
	return out
}
