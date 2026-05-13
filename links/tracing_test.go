package links

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTruncURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"short", "https://example.com/a", "https://example.com/a"},
		{"exactly 256", strings.Repeat("a", 256), strings.Repeat("a", 256)},
		{"257 chars", strings.Repeat("a", 257), strings.Repeat("a", 256)},
		{"way over", strings.Repeat("x", 1024), strings.Repeat("x", 256)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncURL(tc.in)
			if got != tc.want {
				t.Errorf("truncURL(len=%d) = %q (len=%d), want %q (len=%d)",
					len(tc.in), got, len(got), tc.want, len(tc.want))
			}
		})
	}
}

func TestIngestOne_EmitsSpanOnNilStore(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})
	// Re-bind the package tracer to pick up the new global provider.
	tracer = otel.Tracer("darek/links")

	_, _, _, err := IngestOne(context.Background(), nil, Candidate{
		URL:    "https://example.com",
		Source: "user",
	})
	if err == nil {
		t.Fatal("expected error for nil store")
	}

	var names []string
	for _, sp := range exp.GetSpans().Snapshots() {
		names = append(names, sp.Name())
	}
	if len(names) == 0 {
		t.Fatal("expected at least one span")
	}
	found := false
	for _, n := range names {
		if n == "links.ingest_one" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a links.ingest_one span; got %v", names)
	}
}

func TestIngestOne_EmitsSpanOnEmptyURL(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})
	tracer = otel.Tracer("darek/links")

	// Pass a non-nil store via a struct literal — Save will never be called
	// because Canonicalize("") returns "" and IngestOne returns the
	// "unparseable url" error before touching the store.
	store := &Store{}

	_, _, _, err := IngestOne(context.Background(), store, Candidate{URL: ""})
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
	if !strings.Contains(err.Error(), "unparseable") {
		t.Errorf("expected unparseable-url error, got %v", err)
	}
	spans := exp.GetSpans().Snapshots()
	if len(spans) != 1 || spans[0].Name() != "links.ingest_one" {
		t.Fatalf("expected one links.ingest_one span; got %d", len(spans))
	}
	hasUrlRaw := false
	for _, kv := range spans[0].Attributes() {
		if string(kv.Key) == "link.url_raw" {
			hasUrlRaw = true
		}
	}
	if !hasUrlRaw {
		t.Error("expected link.url_raw attribute")
	}
}
