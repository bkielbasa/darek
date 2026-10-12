package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"log/slog"

	"darek/analyze"
	"darek/config"
	"darek/db"
	"darek/exechistory"
	"darek/links"
	"darek/obs"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// runLinks dispatches `darek links <subcmd>`.
func runLinks(ctx context.Context, cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: darek links analyze [--limit N] [--kind article|video]")
	}
	switch args[0] {
	case "analyze":
		return runLinksAnalyze(ctx, cfgPath, args[1:])
	default:
		return fmt.Errorf("unknown links subcommand %q (try: analyze)", args[0])
	}
}

// runLinksAnalyze backfills summary+tags for links that have not been analyzed
// yet. It reuses the same analyzer + tag-vocabulary logic as the ingest
// callback, so results stay consistent.
func runLinksAnalyze(ctx context.Context, cfgPath string, args []string) (retErr error) {
	fs := flag.NewFlagSet("links analyze", flag.ContinueOnError)
	limit := fs.Int("limit", 50, "max links to analyze")
	kind := fs.String("kind", "", "restrict to a link kind (article|video)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
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

	ctx, span := otel.Tracer("darek/cli").Start(ctx, "cli.links.analyze")
	exechistory.MarkExecution(span, "cli-links-analyze")
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	analyzer := buildAnalyzer(cfg)
	if analyzer == nil {
		return fmt.Errorf("openai not configured; cannot analyze links")
	}

	store := links.NewStore(pool)
	rows, err := store.PendingAnalysis(ctx, *kind, *limit)
	if err != nil {
		return fmt.Errorf("pending: %w", err)
	}
	known, err := store.TagVocabulary(ctx, cfg.Analyze.TagVocabLimit)
	if err != nil {
		return fmt.Errorf("tag vocabulary: %w", err)
	}

	analyzed, failed := 0, 0
	for _, l := range rows {
		out, err := analyzer.Analyze(ctx, analyze.Input{
			Title:     l.Title,
			URL:       l.URL,
			Body:      l.Summary,
			KnownTags: known,
		})
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "warn: analyze %s: %v\n", l.URL, err)
			continue
		}
		if err := store.ApplyAnalysis(ctx, l.ID, out.Summary, out.Tags); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "warn: save %s: %v\n", l.URL, err)
			continue
		}
		analyzed++
	}

	fmt.Printf("links analyze: analyzed=%d failed=%d\n", analyzed, failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d links failed", failed, len(rows))
	}
	return nil
}
