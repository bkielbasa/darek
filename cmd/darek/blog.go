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
	"darek/tools/mastodon"
	"darek/tools/todoist"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// buildBlogPublishConfig builds the PublishConfig + a deduped list of
// Todoist project IDs the auto-poster needs to scan. For each (blog,
// platform) pair where a token_env is configured AND its env var is set,
// it constructs the matching Publisher; missing creds → that (blog, platform)
// is silently skipped at publish time (matches drafter-without-poster path).
//
// Returns an error only on transport-level Todoist failures (ResolveProjectID
// network errors) — bad/empty secrets surface as nil publishers, NOT as
// hard config errors, because the user may legitimately set up auto-posting
// for some accounts before others.
func buildBlogPublishConfig(ctx context.Context, bm config.BlogMarketing, td *todoist.Client) (*blogmarketing.PublishConfig, []string, error) {
	pc := blogmarketing.NewPublishConfig()
	seenProjects := map[string]bool{}
	var projectIDs []string

	for _, f := range bm.Feeds {
		projectName := f.ProjectName
		if projectName == "" {
			projectName = bm.ProjectName
		}
		if !seenProjects[projectName] {
			seenProjects[projectName] = true
			pid, err := td.ResolveProjectID(ctx, projectName)
			if err != nil {
				return nil, nil, fmt.Errorf("resolve project %q (blog %s): %w", projectName, f.ID, err)
			}
			projectIDs = append(projectIDs, pid)
		}

		for plat, acc := range f.Accounts {
			if acc.TokenEnv == "" {
				continue // no auto-poster credentials yet for this (blog, platform)
			}
			token, err := config.ResolveSecret("env:" + acc.TokenEnv)
			if err != nil || token == "" {
				// Env var unset or empty: skip silently. Surfaces in
				// `darek doctor` if we extend it; not a hard error here.
				continue
			}
			switch blogmarketing.Platform(plat) {
			case blogmarketing.PlatformMastodon:
				if acc.Instance == "" {
					return nil, nil, fmt.Errorf("blog %s mastodon: token_env is set but instance is missing", f.ID)
				}
				mc, err := mastodon.New(mastodon.Options{Instance: acc.Instance, Token: token})
				if err != nil {
					return nil, nil, fmt.Errorf("blog %s mastodon: %w", f.ID, err)
				}
				pc.Register(f.ID, blogmarketing.PlatformMastodon, blogmarketing.NewMastodonPublisher(mc))
			default:
				// X / LinkedIn auto-posters not implemented yet; configuring
				// token_env is harmless (no publisher registered → skip).
			}
		}
	}
	return pc, projectIDs, nil
}

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
		// Drafter only needs the handle string per platform; instance/token_env
		// are for the auto-poster and resolved elsewhere.
		accounts := make(map[blogmarketing.Platform]string, len(f.Accounts))
		for k, v := range f.Accounts {
			accounts[blogmarketing.Platform(k)] = v.Handle
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
		return fmt.Errorf("usage: darek blog (sync|publish|regenerate)")
	}
	switch args[0] {
	case "sync":
		return runBlogSync(ctx, cfgPath)
	case "publish":
		return runBlogPublish(ctx, cfgPath)
	case "regenerate":
		return runBlogRegenerate(ctx, cfgPath)
	default:
		return fmt.Errorf("unknown blog subcommand %q (try: sync, publish, regenerate)", args[0])
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

// buildBlogRegenerateAccounts converts the config's account map (per-feed
// AccountConfig with handle/instance/token_env) into the per-blog handle-only
// map that the drafter consumes. Mastodon instance / token are stripped
// because the regenerator doesn't post — it only redrafts.
func buildBlogRegenerateAccounts(bm config.BlogMarketing) blogmarketing.RegenerateAccounts {
	out := blogmarketing.RegenerateAccounts{}
	for _, f := range bm.Feeds {
		perBlog := map[blogmarketing.Platform]string{}
		for plat, acc := range f.Accounts {
			perBlog[blogmarketing.Platform(plat)] = acc.Handle
		}
		out[f.ID] = perBlog
	}
	return out
}

func runBlogRegenerate(ctx context.Context, cfgPath string) (retErr error) {
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

	ctx, span := otel.Tracer("darek/cli").Start(ctx, "cli.blog.regenerate")
	exechistory.MarkExecution(span, "cli-blog-regenerate")
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

	store := blogmarketing.NewStore(pool)
	drafter := blogmarketing.NewOpenAIDrafter(llmClient)
	accounts := buildBlogRegenerateAccounts(cfg.BlogMarketing)

	res, err := blogmarketing.Regenerate(ctx, store, td, drafter, accounts)
	if err != nil {
		return fmt.Errorf("regenerate: %w", err)
	}
	fmt.Printf("blog regenerate: regenerated=%d skipped=%d errors=%d\n",
		res.Regenerated, res.Skipped, len(res.Errors))
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "  err: %v\n", e)
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("%d errors during regenerate", len(res.Errors))
	}
	return nil
}

func runBlogPublish(ctx context.Context, cfgPath string) (retErr error) {
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

	ctx, span := otel.Tracer("darek/cli").Start(ctx, "cli.blog.publish")
	exechistory.MarkExecution(span, "cli-blog-publish")
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	tdToken, err := config.ResolveSecret("env:" + cfg.Todoist.TokenEnv)
	if err != nil {
		return fmt.Errorf("todoist token: %w", err)
	}
	td, err := todoist.New(todoist.Options{Token: tdToken})
	if err != nil {
		return fmt.Errorf("todoist client: %w", err)
	}

	pc, projectIDs, err := buildBlogPublishConfig(ctx, cfg.BlogMarketing, td)
	if err != nil {
		return err
	}
	store := blogmarketing.NewStore(pool)

	res, err := blogmarketing.Publish(ctx, store, td, pc, projectIDs)
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	fmt.Printf("blog publish: published=%d completion_retried=%d skipped=%d errors=%d\n",
		res.Published, res.CompletionRetried, res.Skipped, len(res.Errors))
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "  err: %v\n", e)
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("%d errors during publish", len(res.Errors))
	}
	return nil
}
