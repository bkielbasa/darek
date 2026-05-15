package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"log/slog"

	"darek/blogmarketing"
	"darek/cmd/darek/serve"
	"darek/config"
	"darek/db"
	"darek/exechistory"
	"darek/freshrssimport"
	"darek/links"
	"darek/llm"
	"darek/obs"
	"darek/todoistimport"
	"darek/tools/freshrss"
	"darek/tools/todoist"
	"darek/tools/whatsapp"
)

func runServe(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if cfg.Server.Bind == "" {
		cfg.Server.Bind = "127.0.0.1:7777"
	}
	if cfg.FreshRSS.SyncInterval == 0 {
		cfg.FreshRSS.SyncInterval = 15 * time.Minute
	}

	dsn, err := config.ResolveSecret("env:" + cfg.Postgres.URLEnv)
	if err != nil {
		return err
	}

	otelSetup, otelShutdown, err := obs.Init(ctx, obs.Options{
		ServiceName: cfg.OTEL.ServiceName,
		Endpoint:    cfg.OTEL.ExporterEndpoint,
		Insecure:    cfg.OTEL.Insecure,
	})
	if err != nil {
		return fmt.Errorf("otel init: %w", err)
	}
	defer func() { _ = otelShutdown(context.Background()) }()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer pool.Close()

	if err := obs.RegisterPoolGauges(pool); err != nil {
		fmt.Fprintf(os.Stderr, "warn: register pool gauges: %v\n", err)
	}

	var execStore *exechistory.Store
	if cfg.ExecutionHistory.Enabled {
		rec := exechistory.NewRecorder(pool, slog.Default())
		otelSetup.TracerProvider.RegisterSpanProcessor(rec)
		execStore = exechistory.NewStore(pool)
	}

	store := links.NewStore(pool)

	var waManager *whatsapp.Manager
	if cfg.WhatsApp.Enabled {
		var err error
		waManager, err = whatsapp.NewManager(whatsapp.Options{
			StorePath: cfg.WhatsApp.StorePath,
			Pool:      pool,
		})
		if err != nil {
			return fmt.Errorf("whatsapp manager: %w", err)
		}
		go func() {
			if err := waManager.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "whatsapp: %v\n", err)
			}
		}()
	}

	// Build the video-aware analyzer (nil if OpenAI is unconfigured). Used
	// both as the manual-click Analyzer for the HTTP server and as the
	// engine behind the sync auto-analyze callback.
	va := buildVideoAnalyzer(cfg)
	var analyzer serve.Analyzer
	if va != nil {
		analyzer = va
	}
	onVideo := buildVideoAutoAnalyze(va, store)

	// Build the optional sync function — only if FreshRSS is configured.
	var sync serve.SyncFn
	if cfg.FreshRSS.BaseURL != "" {
		password, err := config.ResolveSecret("env:" + cfg.FreshRSS.PasswordEnv)
		if err != nil {
			return fmt.Errorf("freshrss password: %w", err)
		}
		fr, err := freshrss.New(freshrss.Options{
			BaseURL:  cfg.FreshRSS.BaseURL,
			Username: cfg.FreshRSS.Username,
			Password: password,
		})
		if err != nil {
			return fmt.Errorf("freshrss client: %w", err)
		}
		sync = func(ctx context.Context) (string, error) {
			res, err := freshrssimport.Sync(ctx, fr, store, onVideo)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("imported=%d marked_read=%d errors=%d",
				res.Imported, res.MarkedRead, len(res.Errors)), nil
		}
	}

	var todoistSync serve.SyncFn
	if cfg.Todoist.TokenEnv != "" {
		token, err := config.ResolveSecret("env:" + cfg.Todoist.TokenEnv)
		if err == nil && token != "" {
			td, err := todoist.New(todoist.Options{Token: token})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: todoist client: %v\n", err)
			} else {
				todoistSync = func(ctx context.Context) (string, error) {
					res, err := todoistimport.Sync(ctx, td, store, onVideo)
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("imported=%d completed=%d skipped=%d errors=%d",
						res.Imported, res.Completed, res.Skipped, len(res.Errors)), nil
				}
			}
		}
	}

	var blogSync serve.SyncFn
	var blogPublish serve.SyncFn
	var blogRegenerate serve.SyncFn
	if len(cfg.BlogMarketing.Feeds) > 0 {
		apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
		if err != nil {
			return fmt.Errorf("blog_marketing openai key: %w", err)
		}
		llmClient, err := llm.New(llm.Options{APIKey: apiKey, BaseURL: cfg.OpenAI.BaseURL, Model: cfg.OpenAI.Model})
		if err != nil {
			return fmt.Errorf("blog_marketing llm: %w", err)
		}
		tdToken, err := config.ResolveSecret("env:" + cfg.Todoist.TokenEnv)
		if err != nil {
			return fmt.Errorf("blog_marketing todoist token: %w", err)
		}
		td, err := todoist.New(todoist.Options{Token: tdToken})
		if err != nil {
			return fmt.Errorf("blog_marketing todoist: %w", err)
		}
		runs, err := buildBlogFeedRuns(cfg.BlogMarketing)
		if err != nil {
			return err
		}
		bmStore := blogmarketing.NewStore(pool)
		drafter := blogmarketing.NewOpenAIDrafter(llmClient)
		blogSync = func(ctx context.Context) (string, error) {
			res := blogmarketing.SyncAll(ctx, bmStore, drafter, td, runs)
			return fmt.Sprintf("feeds=%d scheduled=%d backfill_seen=%d skipped=%d errors=%d",
				len(runs), res.Scheduled, res.BackfillSeen, res.Skipped, len(res.Errors)), nil
		}

		// Auto-poster: resolve project IDs + per-(blog, platform) tokens once
		// at startup. If any blog has token_env but the env var is empty, that
		// (blog, platform) is silently absent — drafts still go to Todoist for
		// manual posting, just no auto-publish.
		pc, projectIDs, err := buildBlogPublishConfig(ctx, cfg.BlogMarketing, td)
		if err != nil {
			return fmt.Errorf("blog_marketing publish: %w", err)
		}
		blogPublish = func(ctx context.Context) (string, error) {
			res, err := blogmarketing.Publish(ctx, bmStore, td, pc, projectIDs)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("published=%d completion_retried=%d skipped=%d errors=%d",
				res.Published, res.CompletionRetried, res.Skipped, len(res.Errors)), nil
		}

		// Re-roll loop: scan Todoist for the `regenerate` label and rewrite
		// the matching cell. 5min cadence matches the user's mental model of
		// "I labelled it, give me a fresh draft within a few minutes."
		regenAccounts := buildBlogRegenerateAccounts(cfg.BlogMarketing)
		blogRegenerate = func(ctx context.Context) (string, error) {
			res, err := blogmarketing.Regenerate(ctx, bmStore, td, drafter, regenAccounts)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("regenerated=%d skipped=%d errors=%d",
				res.Regenerated, res.Skipped, len(res.Errors)), nil
		}
	}

	clientSecret, err := config.ResolveSecret("env:" + cfg.Auth.ClientSecretEnv)
	if err != nil {
		return fmt.Errorf("auth client secret: %w (set %s)", err, cfg.Auth.ClientSecretEnv)
	}
	sessionKeyHex, err := config.ResolveSecret("env:" + cfg.Auth.SessionKeyEnv)
	if err != nil {
		return fmt.Errorf("auth session key: %w (set %s to `openssl rand -hex 32`)", err, cfg.Auth.SessionKeyEnv)
	}
	sessionKey, err := hex.DecodeString(sessionKeyHex)
	if err != nil {
		return fmt.Errorf("auth session key: not valid hex: %w", err)
	}
	authCfg, err := serve.NewAuthConfig(sessionKey, cfg.Auth.SessionTTL)
	if err != nil {
		return err
	}
	oidcClient, err := serve.NewOIDC(ctx, serve.OIDCConfig{
		Issuer:        cfg.Auth.Issuer,
		ClientID:      cfg.Auth.ClientID,
		ClientSecret:  clientSecret,
		RedirectURL:   cfg.Auth.RedirectURL,
		RequiredGroup: cfg.Auth.RequiredGroup,
	})
	if err != nil {
		return fmt.Errorf("oidc init: %w", err)
	}

	srv, err := serve.New(store, sync, analyzer, authCfg, oidcClient, waManager, execStore, cfg.OTEL.JaegerUIURL)
	if err != nil {
		return err
	}

	// Background sync loops (only if configured AND interval > 0).
	if sync != nil && cfg.FreshRSS.SyncInterval > 0 {
		go runSyncLoop(ctx, sync, cfg.FreshRSS.SyncInterval, "freshrss")
	}
	if todoistSync != nil && cfg.Todoist.SyncInterval > 0 {
		go runSyncLoop(ctx, todoistSync, cfg.Todoist.SyncInterval, "todoist")
	}
	if blogSync != nil && cfg.BlogMarketing.SyncInterval > 0 {
		go runSyncLoop(ctx, blogSync, cfg.BlogMarketing.SyncInterval, "blog")
	}
	// Auto-poster runs on a fixed 1h interval. The user agreed to a hardcoded
	// cadence in the spec; if that proves wrong, add a knob then.
	if blogPublish != nil {
		go runSyncLoop(ctx, blogPublish, time.Hour, "blog-publish")
	}
	if blogRegenerate != nil {
		go runSyncLoop(ctx, blogRegenerate, 5*time.Minute, "blog-regenerate")
	}

	if cfg.ExecutionHistory.Enabled {
		go exechistory.RunCleanup(ctx, pool, cfg.ExecutionHistory, slog.Default())
	}

	fmt.Fprintf(os.Stderr, "darek serve listening on %s\n", cfg.Server.Bind)
	return srv.Run(ctx, cfg.Server.Bind)
}

func runSyncLoop(ctx context.Context, sync serve.SyncFn, interval time.Duration, name string) {
	// Run immediately on startup.
	if msg, err := sync(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s sync error: %v\n", name, err)
	} else {
		fmt.Fprintf(os.Stderr, "%s sync: %s\n", name, msg)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if msg, err := sync(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "%s sync error: %v\n", name, err)
			} else {
				fmt.Fprintf(os.Stderr, "%s sync: %s\n", name, msg)
			}
		}
	}
}
