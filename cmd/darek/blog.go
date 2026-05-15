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

// buildBlogFeedRuns translates config.BlogMarketing into a slice of
// blogmarketing.FeedRun ready for SyncAll. Each feed inherits root-level
// defaults (project_name / post_time / timezone) unless it sets its own.
// Returns an error if a per-feed feed_url is unreachable to construct or a
// timezone is bogus.
func buildBlogFeedRuns(bm config.BlogMarketing) ([]blogmarketing.FeedRun, error) {
	runs := make([]blogmarketing.FeedRun, 0, len(bm.Feeds))
	for _, f := range bm.Feeds {
		project := f.ProjectName
		if project == "" {
			project = bm.ProjectName
		}
		postTime := f.PostTime
		if postTime == "" {
			postTime = bm.PostTime
		}
		tz := f.Timezone
		if tz == "" {
			tz = bm.Timezone
		}
		feed, err := blogfeed.New(blogfeed.Options{URL: f.FeedURL})
		if err != nil {
			return nil, fmt.Errorf("blog %s: feed: %w", f.ID, err)
		}
		var loc *time.Location
		if tz != "" {
			loc, err = time.LoadLocation(tz)
			if err != nil {
				return nil, fmt.Errorf("blog %s: timezone: %w", f.ID, err)
			}
		}
		accounts := make(map[blogmarketing.Platform]string, len(f.Accounts))
		for k, v := range f.Accounts {
			accounts[blogmarketing.Platform(k)] = v
		}
		runs = append(runs, blogmarketing.FeedRun{
			Feed: feed,
			Config: blogmarketing.Config{
				BlogID:      f.ID,
				ProjectName: project,
				PostTime:    postTime,
				Timezone:    loc,
				Accounts:    accounts,
			},
		})
	}
	return runs, nil
}

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
	if len(cfg.BlogMarketing.Feeds) == 0 {
		return fmt.Errorf("blog_marketing.feeds is empty in %s", cfgPath)
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

	runs, err := buildBlogFeedRuns(cfg.BlogMarketing)
	if err != nil {
		return err
	}
	store := blogmarketing.NewStore(pool)
	drafter := blogmarketing.NewOpenAIDrafter(llmClient)

	res := blogmarketing.SyncAll(ctx, store, drafter, td, runs)
	fmt.Printf("blog sync: feeds=%d scheduled=%d backfill_seen=%d skipped=%d errors=%d\n",
		len(runs), res.Scheduled, res.BackfillSeen, res.Skipped, len(res.Errors))
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "  err: %v\n", e)
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("%d errors during sync", len(res.Errors))
	}
	return nil
}
