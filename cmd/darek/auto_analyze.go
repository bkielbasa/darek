package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"darek/analyze"
	"darek/config"
	"darek/links"
	"darek/llm"
	"darek/obs"
	"darek/tools/youtube"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// buildAnalyzer constructs a VideoAwareAnalyzer wired to a real youtube.Client
// and an OpenAI-backed *Analyzer. Returns nil (and logs to stderr) if OpenAI is
// unconfigured.
func buildAnalyzer(cfg *config.Config) *analyze.VideoAwareAnalyzer {
	apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
	if err != nil || apiKey == "" {
		fmt.Fprintf(os.Stderr, "info: openai not configured, link analyze disabled\n")
		return nil
	}
	llmClient, err := llm.New(llm.Options{
		APIKey:  apiKey,
		BaseURL: cfg.OpenAI.BaseURL,
		Model:   cfg.OpenAI.Model,
		Timeout: cfg.Agent.LLMTimeout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: llm client: %v (link analyze disabled)\n", err)
		return nil
	}
	ytClient := youtube.NewClient(&http.Client{Timeout: 15 * time.Second})
	return analyze.NewVideoAware(analyze.New(llmClient), ytClient)
}

// buildAutoAnalyze returns a callback suitable for freshrssimport.OnIngestedFunc
// / todoistimport.OnIngestedFunc. Returns nil when the analyzer is nil or
// analyze is disabled — sync packages no-op the callback path.
//
// The returned function skips kinds not listed in cfg.Analyze.Kinds, feeds the
// existing tag vocabulary to the analyzer (so tags are reused, not reinvented),
// persists summary+tags via store.ApplyAnalysis, and increments
// darek.links.analyze.
func buildAutoAnalyze(cfg *config.Config, a *analyze.VideoAwareAnalyzer, store *links.Store) func(ctx context.Context, id uuid.UUID, kind, url, title, body string) error {
	if a == nil || !cfg.Analyze.Enabled {
		return nil
	}
	kinds := make(map[string]struct{}, len(cfg.Analyze.Kinds))
	for _, k := range cfg.Analyze.Kinds {
		kinds[k] = struct{}{}
	}
	return func(ctx context.Context, id uuid.UUID, kind, url, title, body string) error {
		if _, ok := kinds[kind]; !ok {
			return nil
		}
		known, err := store.TagVocabulary(ctx, cfg.Analyze.TagVocabLimit)
		if err != nil {
			recordSyncAnalyze(ctx, kind, "error")
			return err
		}
		out, err := a.Analyze(ctx, analyze.Input{Title: title, URL: url, Body: body, KnownTags: known})
		if err != nil {
			recordSyncAnalyze(ctx, kind, "error")
			return err
		}
		if err := store.ApplyAnalysis(ctx, id, out.Summary, out.Tags); err != nil {
			recordSyncAnalyze(ctx, kind, "error")
			return err
		}
		recordSyncAnalyze(ctx, kind, "ok")
		return nil
	}
}

func recordSyncAnalyze(ctx context.Context, kind, outcome string) {
	m, _ := obs.MetricsInstance()
	if m == nil {
		return
	}
	m.LinksAnalyze.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", outcome),
		attribute.String("trigger", "sync"),
		attribute.String("kind", kind),
	))
}
