package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"darek/agent"
	"darek/config"
	"darek/db"
	"darek/llm"
	"darek/memory"
	"darek/obs"
	"darek/tools"
	"darek/tools/calendar"
	googlecal "darek/tools/calendar/google"
	"darek/tools/calendar/ical"
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
