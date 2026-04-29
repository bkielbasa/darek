package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"darek/cmd/darek/serve"
	"darek/config"
	"darek/db"
	"darek/freshrssimport"
	"darek/links"
	"darek/obs"
	"darek/tools/freshrss"
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

	_, otelShutdown, err := obs.Init(ctx, obs.Options{
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

	store := links.NewStore(pool)

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
			res, err := freshrssimport.Sync(ctx, fr, store)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("imported=%d marked_read=%d errors=%d",
				res.Imported, res.MarkedRead, len(res.Errors)), nil
		}
	}

	srv, err := serve.New(store, sync)
	if err != nil {
		return err
	}

	// Background sync loop (only if sync is configured AND interval > 0).
	if sync != nil && cfg.FreshRSS.SyncInterval > 0 {
		go runSyncLoop(ctx, sync, cfg.FreshRSS.SyncInterval)
	}

	fmt.Fprintf(os.Stderr, "darek serve listening on %s\n", cfg.Server.Bind)
	return srv.Run(ctx, cfg.Server.Bind)
}

func runSyncLoop(ctx context.Context, sync serve.SyncFn, interval time.Duration) {
	// Run immediately on startup.
	if msg, err := sync(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "freshrss sync error: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "freshrss sync: %s\n", msg)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if msg, err := sync(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "freshrss sync error: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "freshrss sync: %s\n", msg)
			}
		}
	}
}
