package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"darek/agent"
	"darek/config"
	"darek/db"
	"darek/links"
	"darek/llm"
	"darek/memory"
	"darek/obs"
	"darek/tools"
	"darek/tools/calendar"
	googlecal "darek/tools/calendar/google"
	"darek/tools/calendar/ical"
	"darek/tools/mail"
	mailimap "darek/tools/mail/imap"
	mailsmtp "darek/tools/mail/smtp"
	"darek/tools/todoist"
	"darek/tools/freshrss"
)

func runChat(ctx context.Context, cfgPath, userInput string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
	if err != nil {
		return fmt.Errorf("openai key: %w", err)
	}
	dsn, err := config.ResolveSecret("env:" + cfg.Postgres.URLEnv)
	if err != nil {
		return fmt.Errorf("postgres dsn: %w", err)
	}

	_, otelShutdown, err := obs.Init(ctx, obs.Options{
		ServiceName: cfg.OTEL.ServiceName,
		Endpoint:    cfg.OTEL.ExporterEndpoint,
		Insecure:    cfg.OTEL.Insecure,
	})
	if err != nil {
		return fmt.Errorf("otel init: %w", err)
	}
	defer func() { _ = otelShutdown(context.Background()) }()
	logger := obs.NewLogger(cfg.OTEL.ServiceName)

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer pool.Close()

	llmClient, err := llm.New(llm.Options{
		APIKey:  apiKey,
		BaseURL: cfg.OpenAI.BaseURL,
		Model:   cfg.OpenAI.Model,
		Timeout: cfg.Agent.LLMTimeout,
	})
	if err != nil {
		return err
	}

	reg, err := tools.NewRegistry(cfg.Agent.ToolTimeout)
	if err != nil {
		return err
	}
	store := memory.NewStore(pool)
	if err := reg.Register(memory.RecallTool{Store: store}); err != nil {
		return err
	}
	if err := reg.Register(memory.SaveTool{Store: store}); err != nil {
		return err
	}

	// Links (taste graph)
	linkStore := links.NewStore(pool)
	for _, t := range []tools.Tool{
		links.SaveTool{Store: linkStore},
		links.SearchTool{Store: linkStore},
		links.SimilarTool{Store: linkStore},
	} {
		if err := reg.Register(t); err != nil {
			return err
		}
	}

	// Calendar sources
	if len(cfg.Calendars) > 0 {
		srcs := calendar.NewSources()
		home, _ := os.UserHomeDir()
		store := googlecal.NewTokenStore(filepath.Join(home, ".darek", "oauth"))
		for _, c := range cfg.Calendars {
			switch c.Kind {
			case "ical":
				if err := srcs.Add(ical.New(c.Nickname, c.URL)); err != nil {
					return fmt.Errorf("calendar %s: %w", c.Nickname, err)
				}
			case "google":
				cid, err := config.ResolveSecret("env:" + c.ClientIDEnv)
				if err != nil {
					logger.WarnContext(ctx, "skipping google calendar", "nickname", c.Nickname, "error", err.Error())
					continue
				}
				cs, err := config.ResolveSecret("env:" + c.ClientSecretEnv)
				if err != nil {
					logger.WarnContext(ctx, "skipping google calendar", "nickname", c.Nickname, "error", err.Error())
					continue
				}
				oauthCfg := googlecal.Config(cid, cs)
				if err := srcs.Add(googlecal.NewSource(c.Nickname, c.CalendarID, oauthCfg, store)); err != nil {
					return fmt.Errorf("calendar %s: %w", c.Nickname, err)
				}
			default:
				logger.WarnContext(ctx, "unknown calendar kind", "kind", c.Kind, "nickname", c.Nickname)
			}
		}
		if len(srcs.Names()) > 0 {
			if err := reg.Register(calendar.ListEventsTool{Sources: srcs}); err != nil {
				return err
			}
		}
	}

	// Todoist
	if cfg.Todoist.TokenEnv != "" {
		if tok, err := config.ResolveSecret("env:" + cfg.Todoist.TokenEnv); err != nil {
			logger.WarnContext(ctx, "skipping todoist", "error", err.Error())
		} else {
			tdc, err := todoist.New(todoist.Options{Token: tok})
			if err != nil {
				return fmt.Errorf("todoist: %w", err)
			}
			for _, t := range []tools.Tool{
				todoist.ListTool{Client: tdc},
				todoist.CreateTool{Client: tdc},
				todoist.CompleteTool{Client: tdc},
				todoist.UpdateTool{Client: tdc},
			} {
				if err := reg.Register(t); err != nil {
					return err
				}
			}
		}
	}

	// FreshRSS
	if cfg.FreshRSS.BaseURL != "" && cfg.FreshRSS.PasswordEnv != "" {
		if pw, err := config.ResolveSecret("env:" + cfg.FreshRSS.PasswordEnv); err != nil {
			logger.WarnContext(ctx, "skipping freshrss", "error", err.Error())
		} else {
			frc, err := freshrss.New(freshrss.Options{
				BaseURL: cfg.FreshRSS.BaseURL, Username: cfg.FreshRSS.Username, Password: pw,
			})
			if err != nil {
				return fmt.Errorf("freshrss: %w", err)
			}
			for _, t := range []tools.Tool{
				freshrss.ListTool{Client: frc},
				freshrss.GetTool{Client: frc},
				freshrss.MarkTool{Client: frc},
			} {
				if err := reg.Register(t); err != nil {
					return err
				}
			}
		}
	}

	// Mail tools
	if len(cfg.Mail.Accounts) > 0 {
		mstore := mail.NewStore(pool)
		resolver := mailAccountResolver{}
		for _, ac := range cfg.Mail.Accounts {
			secret, err := config.ResolveSecret("env:" + ac.SecretEnv)
			if err != nil {
				logger.WarnContext(ctx, "skipping mail account", "nickname", ac.Nickname, "error", err.Error())
				continue
			}
			resolver[ac.Nickname] = mailimap.New(mailimap.Options{
				Nickname: ac.Nickname, Email: ac.Email,
				Host: ac.IMAP.Host, Port: ac.IMAP.Port, TLS: ac.IMAP.TLS,
				Username: ac.Username, Password: secret,
			})
		}
		attDir := expandHomeChat(cfg.Mail.AttachmentsDir)
		for _, t := range []tools.Tool{
			mail.SearchTool{Store: mstore},
			mail.GetBodyTool{Store: mstore, Accounts: resolver},
			mail.GetAttachmentTool{Store: mstore, Accounts: resolver, AttachmentsDir: attDir},
		} {
			if err := reg.Register(t); err != nil {
				return err
			}
		}

		// Send tool: build per-account SendDeps from SMTP + IMAP append capability.
		sendResolver := mailSendResolver{}
		for _, ac := range cfg.Mail.Accounts {
			secret, err := config.ResolveSecret("env:" + ac.SecretEnv)
			if err != nil {
				continue // already warned above
			}
			imapAcc, ok := resolver[ac.Nickname]
			if !ok {
				continue
			}
			var sndr mail.Sender
			if ac.SMTP.Host != "" {
				sndr = mailsmtp.New(mailsmtp.Options{
					Host: ac.SMTP.Host, Port: ac.SMTP.Port, TLS: ac.SMTP.TLS,
					Username: ac.Username, Password: secret,
				})
			}
			var app mail.Appender
			if ia, ok := imapAcc.(*mailimap.Account); ok {
				app = ia
			}
			sendResolver[ac.Nickname] = mail.SendDeps{
				From: ac.Email, SMTP: sndr, Appender: app,
				SentFolder: "Sent", Hostname: "darek.local",
			}
		}
		if err := reg.Register(mail.SendTool{
			Store: mstore, Accounts: sendResolver, Confirm: mail.NewCLIConfirmer(),
		}); err != nil {
			return err
		}
	}

	a, err := agent.New(agent.Options{
		LLM: llmClient, Tools: reg, MaxIterations: cfg.Agent.MaxIterations,
	})
	if err != nil {
		return err
	}

	if userInput == "" {
		userInput, err = readStdin()
		if err != nil {
			return err
		}
	}
	if userInput == "" {
		return errors.New("empty input (pass a prompt or pipe stdin)")
	}

	res, err := a.RunTurn(ctx, userInput)
	if err != nil {
		return err
	}
	fmt.Println(res.Output)
	logger.InfoContext(ctx, "turn complete", "iterations", res.Iterations)
	return nil
}

func readStdin() (string, error) {
	st, _ := os.Stdin.Stat()
	if st.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var b []byte
	for sc.Scan() {
		b = append(b, sc.Bytes()...)
		b = append(b, '\n')
	}
	return string(b), sc.Err()
}

type mailAccountResolver map[string]mail.MailAccount

func (m mailAccountResolver) ByNickname(n string) (mail.MailAccount, bool) {
	a, ok := m[n]
	return a, ok
}

type mailSendResolver map[string]mail.SendDeps

func (m mailSendResolver) SendDepsFor(n string) (mail.SendDeps, bool) {
	d, ok := m[n]
	return d, ok
}

func expandHomeChat(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
