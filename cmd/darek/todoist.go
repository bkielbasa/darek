package main

import (
	"context"
	"fmt"
	"os"

	"darek/config"
	"darek/db"
	"darek/exechistory"
	"darek/links"
	"darek/obs"
	"darek/todoistimport"
	"darek/tools/todoist"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// runTodoist dispatches `darek todoist <subcmd>`.
func runTodoist(ctx context.Context, cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: darek todoist sync")
	}
	switch args[0] {
	case "sync":
		return runTodoistSync(ctx, cfgPath)
	default:
		return fmt.Errorf("unknown todoist subcommand %q (try: sync)", args[0])
	}
}

func runTodoistSync(ctx context.Context, cfgPath string) (retErr error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if cfg.Todoist.TokenEnv == "" {
		return fmt.Errorf("todoist not configured in %s", cfgPath)
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

	ctx, span := otel.Tracer("darek/cli").Start(ctx, "cli.todoist.sync")
	exechistory.MarkExecution(span, "cli-todoist-sync")
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer pool.Close()

	if err := obs.RegisterPoolGauges(pool); err != nil {
		fmt.Fprintf(os.Stderr, "warn: register pool gauges: %v\n", err)
	}

	token, err := config.ResolveSecret("env:" + cfg.Todoist.TokenEnv)
	if err != nil {
		return fmt.Errorf("todoist token: %w", err)
	}
	td, err := todoist.New(todoist.Options{Token: token})
	if err != nil {
		return fmt.Errorf("todoist client: %w", err)
	}

	store := links.NewStore(pool)
	va := buildVideoAnalyzer(cfg)
	onVideo := buildVideoAutoAnalyze(va, store)
	res, err := todoistimport.Sync(ctx, td, store, onVideo)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	fmt.Printf("todoist sync: imported=%d completed=%d skipped=%d errors=%d\n",
		res.Imported, res.Completed, res.Skipped, len(res.Errors))
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "  err: %v\n", e)
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("%d errors during sync", len(res.Errors))
	}
	return nil
}
