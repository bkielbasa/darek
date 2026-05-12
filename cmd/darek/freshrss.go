package main

import (
	"context"
	"fmt"
	"os"

	"log/slog"

	"darek/config"
	"darek/db"
	"darek/exechistory"
	"darek/freshrssimport"
	"darek/links"
	"darek/obs"
	"darek/tools/freshrss"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// runFreshRSS dispatches `darek freshrss <subcmd>`.
func runFreshRSS(ctx context.Context, cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: darek freshrss sync")
	}
	switch args[0] {
	case "sync":
		return runFreshRSSSync(ctx, cfgPath)
	default:
		return fmt.Errorf("unknown freshrss subcommand %q (try: sync)", args[0])
	}
}

func runFreshRSSSync(ctx context.Context, cfgPath string) (retErr error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if cfg.FreshRSS.BaseURL == "" {
		return fmt.Errorf("freshrss not configured in %s", cfgPath)
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

	if cfg.ExecutionHistory.Enabled {
		otelSetup.TracerProvider.RegisterSpanProcessor(
			exechistory.NewRecorder(pool, slog.Default()))
	}

	ctx, span := otel.Tracer("darek/cli").Start(ctx, "cli.freshrss.sync")
	exechistory.MarkExecution(span, "cli-freshrss-sync")
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	if err := obs.RegisterPoolGauges(pool); err != nil {
		fmt.Fprintf(os.Stderr, "warn: register pool gauges: %v\n", err)
	}

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

	store := links.NewStore(pool)
	va := buildVideoAnalyzer(cfg)
	onVideo := buildVideoAutoAnalyze(va, store)
	res, err := freshrssimport.Sync(ctx, fr, store, onVideo)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	fmt.Printf("freshrss sync: imported=%d marked_read=%d skipped=%d errors=%d\n",
		res.Imported, res.MarkedRead, res.Skipped, len(res.Errors))
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "  err: %v\n", e)
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("%d errors during sync", len(res.Errors))
	}
	return nil
}
