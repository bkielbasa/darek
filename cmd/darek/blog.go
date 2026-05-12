package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"log/slog"

	"darek/blogmarketing"
	"darek/config"
	"darek/db"
	"darek/exechistory"
	"darek/llm"
	"darek/obs"
	"darek/tools/blogfeed"
	"darek/tools/todoist"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

func runBlog(ctx context.Context, cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: darek blog sync")
	}
	switch args[0] {
	case "sync":
		return runBlogSync(ctx, cfgPath)
	default:
		return fmt.Errorf("unknown blog subcommand %q (try: sync)", args[0])
	}
}

func runBlogSync(ctx context.Context, cfgPath string) (retErr error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if cfg.BlogMarketing.FeedURL == "" {
		return fmt.Errorf("blog_marketing not configured in %s", cfgPath)
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

	ctx, span := otel.Tracer("darek/cli").Start(ctx, "cli.blog.sync")
	exechistory.MarkExecution(span, "cli-blog-sync")
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
	if err != nil {
		return fmt.Errorf("openai key: %w", err)
	}
	llmClient, err := llm.New(llm.Options{APIKey: apiKey, BaseURL: cfg.OpenAI.BaseURL, Model: cfg.OpenAI.Model})
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}

	tdToken, err := config.ResolveSecret("env:" + cfg.Todoist.TokenEnv)
	if err != nil {
		return fmt.Errorf("todoist token: %w", err)
	}
	td, err := todoist.New(todoist.Options{Token: tdToken})
	if err != nil {
		return fmt.Errorf("todoist client: %w", err)
	}

	feed, err := blogfeed.New(blogfeed.Options{URL: cfg.BlogMarketing.FeedURL})
	if err != nil {
		return fmt.Errorf("blogfeed client: %w", err)
	}
	store := blogmarketing.NewStore(pool)
	drafter := blogmarketing.NewOpenAIDrafter(llmClient)

	bcfg := blogmarketing.Config{
		FeedURL:      cfg.BlogMarketing.FeedURL,
		ProjectName:  cfg.BlogMarketing.ProjectName,
		PostTime:     cfg.BlogMarketing.PostTime,
		SyncInterval: cfg.BlogMarketing.SyncInterval,
	}
	if cfg.BlogMarketing.Timezone != "" {
		loc, err := time.LoadLocation(cfg.BlogMarketing.Timezone)
		if err != nil {
			return fmt.Errorf("timezone: %w", err)
		}
		bcfg.Timezone = loc
	}

	res, err := blogmarketing.Sync(ctx, feed, store, drafter, td, bcfg)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	fmt.Printf("blog sync: scheduled=%d backfill_seen=%d skipped=%d errors=%d\n",
		res.Scheduled, res.BackfillSeen, res.Skipped, len(res.Errors))
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "  err: %v\n", e)
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("%d errors during sync", len(res.Errors))
	}
	return nil
}
