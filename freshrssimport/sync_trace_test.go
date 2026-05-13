package freshrssimport

import (
	"context"
	"testing"

	"darek/tools/freshrss"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestProcessArticle_EmitsSpanOnEmptyURL(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})
	// Re-bind the package tracer to pick up the new global provider.
	tracer = otel.Tracer("darek/freshrssimport")

	res := processArticle(context.Background(), nil, nil,
		freshrss.Article{ID: "x", URL: ""}, nil)
	if !res.Skipped {
		t.Fatal("expected Skipped outcome for empty URL")
	}

	spans := exp.GetSpans().Snapshots()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name() != "freshrssimport.process_article" {
		t.Errorf("span name = %q, want freshrssimport.process_article", spans[0].Name())
	}
	hasArticleID := false
	for _, kv := range spans[0].Attributes() {
		if string(kv.Key) == "article.id" {
			hasArticleID = true
		}
	}
	if !hasArticleID {
		t.Error("expected article.id attribute")
	}
}
