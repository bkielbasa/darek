package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"darek/config"
	"darek/tools/calendar"
	"darek/tools/calendar/digest"
	googlecal "darek/tools/calendar/google"
	"darek/tools/calendar/ical"
	mailsmtp "darek/tools/mail/smtp"
)

// runDailyDigest sends a 3-day calendar digest email.
// Subcommand: `darek calendar daily-digest`.
func runDailyDigest(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	d := cfg.CalendarDigest
	if d.To == "" {
		return fmt.Errorf("calendar_digest.to is required")
	}
	if d.FromAccount == "" {
		return fmt.Errorf("calendar_digest.from_account is required")
	}
	if len(cfg.Calendars) == 0 {
		return fmt.Errorf("no calendars configured")
	}

	var mailAcct *config.MailAccountCfg
	for i := range cfg.Mail.Accounts {
		if cfg.Mail.Accounts[i].Nickname == d.FromAccount {
			mailAcct = &cfg.Mail.Accounts[i]
			break
		}
	}
	if mailAcct == nil {
		return fmt.Errorf("calendar_digest.from_account %q not found among mail.accounts", d.FromAccount)
	}
	smtpPassword, err := config.ResolveSecret("env:" + mailAcct.SecretEnv)
	if err != nil {
		return fmt.Errorf("smtp secret for %s: %w", mailAcct.Nickname, err)
	}

	srcs := calendar.NewSources()
	home, _ := os.UserHomeDir()
	tokenStore := googlecal.NewTokenStore(filepath.Join(home, ".darek", "oauth"))
	for _, c := range cfg.Calendars {
		switch c.Kind {
		case "ical":
			if err := srcs.Add(ical.New(c.Nickname, c.URL)); err != nil {
				return fmt.Errorf("calendar %s: %w", c.Nickname, err)
			}
		case "google":
			cid, err := config.ResolveSecret("env:" + c.ClientIDEnv)
			if err != nil {
				return fmt.Errorf("calendar %s client id: %w", c.Nickname, err)
			}
			cs, err := config.ResolveSecret("env:" + c.ClientSecretEnv)
			if err != nil {
				return fmt.Errorf("calendar %s client secret: %w", c.Nickname, err)
			}
			oauthCfg := googlecal.Config(cid, cs)
			if err := srcs.Add(googlecal.NewSource(c.Nickname, c.CalendarID, oauthCfg, tokenStore)); err != nil {
				return fmt.Errorf("calendar %s: %w", c.Nickname, err)
			}
		default:
			return fmt.Errorf("unknown calendar kind %q for nickname %q", c.Kind, c.Nickname)
		}
	}

	now := time.Now()
	from, to := digest.Window(now)
	events, err := srcs.ListEvents(ctx, from, to, "")
	if err != nil {
		return fmt.Errorf("list events: %w", err)
	}

	buckets := digest.Group(events, from, to)
	text := digest.RenderText(buckets)
	html := digest.RenderHTML(buckets, now)

	subject := d.Subject
	if subject == "" {
		subject = "Calendar — " + from.Format("2006-01-02")
	} else {
		subject = strings.ReplaceAll(subject, "{{date}}", from.Format("2006-01-02"))
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "darek.local"
	}
	raw, err := digest.BuildEmail(digest.EmailInput{
		From:     mailAcct.Email,
		To:       d.To,
		Subject:  subject,
		Text:     text,
		HTML:     html,
		Date:     now,
		Hostname: hostname,
	})
	if err != nil {
		return fmt.Errorf("build digest email: %w", err)
	}

	sender := mailsmtp.New(mailsmtp.Options{
		Host:     mailAcct.SMTP.Host,
		Port:     mailAcct.SMTP.Port,
		TLS:      mailAcct.SMTP.TLS,
		Username: mailAcct.Username,
		Password: smtpPassword,
	})
	if err := sender.Send(ctx, mailAcct.Email, []string{d.To}, raw); err != nil {
		return fmt.Errorf("send digest: %w", err)
	}
	fmt.Fprintf(os.Stderr, "sent calendar digest to %s (window %s..%s)\n",
		d.To, from.Format("2006-01-02"), to.Format("2006-01-02"))
	return nil
}
