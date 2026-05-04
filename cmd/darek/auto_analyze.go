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

// buildVideoAnalyzer constructs a VideoAwareAnalyzer wired to a real
// youtube.Client and an OpenAI-backed *Analyzer. Returns nil (and logs to
// stderr) if OpenAI is unconfigured.
func buildVideoAnalyzer(cfg *config.Config) *analyze.VideoAwareAnalyzer {
	apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
	if err != nil || apiKey == "" {
		fmt.Fprintf(os.Stderr, "info: openai not configured, video analyze disabled\n")
		return nil
	}
	llmClient, err := llm.New(llm.Options{
		APIKey:  apiKey,
		BaseURL: cfg.OpenAI.BaseURL,
		Model:   cfg.OpenAI.Model,
		Timeout: cfg.Agent.LLMTimeout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: llm client: %v (video analyze disabled)\n", err)
		return nil
	}
	ytClient := youtube.NewClient(&http.Client{Timeout: 15 * time.Second})
	return analyze.NewVideoAware(analyze.New(llmClient), ytClient)
}

// buildVideoAutoAnalyze returns a callback suitable for
// freshrssimport.OnVideoIngestedFunc / todoistimport.OnVideoIngestedFunc.
// Returns nil if va is nil — sync packages no-op the callback path.
//
// The returned function calls the analyzer, persists summary+tags via
// store.ApplyAnalysis, and increments darek.links.analyze with
// trigger="sync_video".
func buildVideoAutoAnalyze(va *analyze.VideoAwareAnalyzer, store *links.Store) func(ctx context.Context, id uuid.UUID, url, title string) error {
	if va == nil {
		return nil
	}
	return func(ctx context.Context, id uuid.UUID, url, title string) error {
		out, err := va.Analyze(ctx, analyze.Input{Title: title, URL: url})
		if err != nil {
			recordSyncAnalyze(ctx, "error")
			return err
		}
		if err := store.ApplyAnalysis(ctx, id, out.Summary, out.Tags); err != nil {
			recordSyncAnalyze(ctx, "error")
			return err
		}
		recordSyncAnalyze(ctx, "ok")
		return nil
	}
}

func recordSyncAnalyze(ctx context.Context, outcome string) {
	m, _ := obs.MetricsInstance()
	if m == nil {
		return
	}
	m.LinksAnalyze.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", outcome),
		attribute.String("trigger", "sync_video"),
	))
}
