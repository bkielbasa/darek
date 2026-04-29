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

	store := mail.NewStore(pool.Inner())
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

	if cfg.Mail.AttachmentTTLDays > 0 && cfg.Mail.AttachmentsDir != "" {
		_ = mail.GCAttachments(expandHomeMail(cfg.Mail.AttachmentsDir), cfg.Mail.AttachmentTTLDays)
	}
	return nil
}

func expandHomeMail(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
