package smtp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
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
	opts   Options
	tracer trace.Tracer
}

// New creates a new Sender. If opts.Timeout is zero, it defaults to 30s.
func New(opts Options) *Sender {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	return &Sender{opts: opts, tracer: otel.Tracer("darek/mail/smtp")}
}

// Send delivers raw RFC 5322 bytes from `from` to each recipient.
func (s *Sender) Send(ctx context.Context, from string, recipients []string, raw []byte) error {
	_, span := s.tracer.Start(ctx, "smtp.send",
		trace.WithAttributes(
			attribute.String("smtp.host", s.opts.Host),
			attribute.Int("smtp.port", s.opts.Port),
			attribute.Bool("smtp.tls", s.opts.TLS),
			attribute.Int("smtp.recipient_count", len(recipients)),
			attribute.Int("smtp.bytes", len(raw)),
		),
	)
	defer span.End()

	addr := net.JoinHostPort(s.opts.Host, fmt.Sprint(s.opts.Port))
	dialer := &net.Dialer{Timeout: s.opts.Timeout}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		err = fmt.Errorf("smtp dial %s: %w", addr, err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	var c *smtp.Client
	if s.opts.TLS {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: s.opts.Host})
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			err = fmt.Errorf("tls handshake: %w", err)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
		c, err = smtp.NewClient(tlsConn, s.opts.Host)
	} else {
		c, err = smtp.NewClient(conn, s.opts.Host)
	}
	if err != nil {
		_ = conn.Close()
		err = fmt.Errorf("smtp new client: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	defer func() { _ = c.Quit() }()

	if !s.opts.TLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.opts.Host}); err != nil {
				err = fmt.Errorf("starttls: %w", err)
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				return err
			}
		}
	}

	if s.opts.Username != "" {
		auth := smtp.PlainAuth("", s.opts.Username, s.opts.Password, s.opts.Host)
		if err := c.Auth(auth); err != nil {
			err = fmt.Errorf("smtp auth: %w", err)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
	}

	if err := c.Mail(from); err != nil {
		err = fmt.Errorf("MAIL FROM: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	for _, r := range recipients {
		if err := c.Rcpt(r); err != nil {
			err = fmt.Errorf("RCPT TO %s: %w", r, err)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
	}

	w, err := c.Data()
	if err != nil {
		err = fmt.Errorf("DATA: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	if _, err := w.Write(raw); err != nil {
		err = fmt.Errorf("write body: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	if err := w.Close(); err != nil {
		err = fmt.Errorf("close data writer: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	return nil
}
