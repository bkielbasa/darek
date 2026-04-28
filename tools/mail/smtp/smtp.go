package smtp

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"time"
)

// Options holds configuration for the SMTP sender.
type Options struct {
	Host     string
	Port     int
	TLS      bool
	Username string
	Password string
	Timeout  time.Duration
}

// Sender delivers mail via SMTP.
type Sender struct {
	opts Options
}

// New creates a new Sender. If opts.Timeout is zero, it defaults to 30s.
func New(opts Options) *Sender {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	return &Sender{opts: opts}
}

// Send delivers raw RFC 5322 bytes from `from` to each recipient.
func (s *Sender) Send(from string, recipients []string, raw []byte) error {
	addr := net.JoinHostPort(s.opts.Host, fmt.Sprint(s.opts.Port))
	dialer := &net.Dialer{Timeout: s.opts.Timeout}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}

	var c *smtp.Client
	if s.opts.TLS {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: s.opts.Host})
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return fmt.Errorf("tls handshake: %w", err)
		}
		c, err = smtp.NewClient(tlsConn, s.opts.Host)
	} else {
		c, err = smtp.NewClient(conn, s.opts.Host)
	}
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp new client: %w", err)
	}
	defer func() { _ = c.Quit() }()

	if !s.opts.TLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.opts.Host}); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		}
	}

	if s.opts.Username != "" {
		auth := smtp.PlainAuth("", s.opts.Username, s.opts.Password, s.opts.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := c.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, r := range recipients {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", r, err)
		}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close data writer: %w", err)
	}

	return nil
}
