package exechistory

import (
	"context"
	"encoding/json"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// finishedSpan ends a span via the SDK and returns the corresponding
// ReadOnlySpan from an in-memory exporter, which is what OnEnd would see.
func finishedSpan(t *testing.T, tracerName, spanName string, setup func(span sdktrace.ReadWriteSpan)) sdktrace.ReadOnlySpan {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tr := tp.Tracer(tracerName)
	_, s := tr.Start(context.Background(), spanName)
	if setup != nil {
		setup(s.(sdktrace.ReadWriteSpan))
	}
	s.End()
	spans := exp.GetSpans().Snapshots()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	return spans[0]
}

func TestSpanToExecutionRow_BasicMapping(t *testing.T) {
	s := finishedSpan(t, "darek/freshrssimport", "freshrssimport.sync", func(span sdktrace.ReadWriteSpan) {
		span.SetAttributes(
			attribute.String(KindAttribute, "freshrss-sync"),
			attribute.Int("freshrss.imported", 7),
			attribute.String("db.statement", "SELECT 1"), // must be filtered out
		)
	})

	row, err := spanToExecutionRow(s, llmTotals{})
	if err != nil {
		t.Fatal(err)
	}
	if row.Kind != "freshrss-sync" {
		t.Errorf("kind: got %q want freshrss-sync", row.Kind)
	}
	if row.Name != "freshrssimport.sync" {
		t.Errorf("name: got %q", row.Name)
	}
	if row.Status != "ok" {
		t.Errorf("status: got %q want ok", row.Status)
	}

	// Decode attributes JSON to verify filtering.
	var attrs map[string]any
	if err := json.Unmarshal(row.Attributes, &attrs); err != nil {
		t.Fatal(err)
	}
	if got, ok := attrs["freshrss.imported"]; !ok || int64FromAny(got) != 7 {
		t.Errorf("expected freshrss.imported=7, got %v", attrs["freshrss.imported"])
	}
	if _, ok := attrs["db.statement"]; ok {
		t.Error("db.statement should have been filtered out")
	}
	if row.DurationMS < 0 {
		t.Errorf("duration_ms: got %d", row.DurationMS)
	}
}

func TestSpanToExecutionRow_ErrorStatus(t *testing.T) {
	s := finishedSpan(t, "darek/freshrssimport", "freshrssimport.sync", func(span sdktrace.ReadWriteSpan) {
		span.SetAttributes(attribute.String(KindAttribute, "freshrss-sync"))
		span.SetStatus(codes.Error, "boom")
	})
	row, err := spanToExecutionRow(s, llmTotals{})
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "error" {
		t.Errorf("status: got %q want error", row.Status)
	}
	if row.Error != "boom" {
		t.Errorf("error: got %q want boom", row.Error)
	}
}

func TestExecutionKind_PresentAndAbsent(t *testing.T) {
	with := finishedSpan(t, "darek/x", "n", func(span sdktrace.ReadWriteSpan) {
		span.SetAttributes(attribute.String(KindAttribute, "thing"))
	})
	if k, ok := executionKind(with); !ok || k != "thing" {
		t.Errorf("executionKind(with): got (%q,%v) want (thing,true)", k, ok)
	}
	without := finishedSpan(t, "darek/x", "n", nil)
	if _, ok := executionKind(without); ok {
		t.Error("executionKind(without): expected ok=false")
	}
}

func TestSpanToStepRow_CapturesParent(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tr := tp.Tracer("darek/x")
	ctx, parent := tr.Start(context.Background(), "parent")
	_, child := tr.Start(ctx, "child")
	child.End()
	parent.End()

	snaps := exp.GetSpans().Snapshots()
	var childSnap sdktrace.ReadOnlySpan
	for _, s := range snaps {
		if s.Name() == "child" {
			childSnap = s
		}
	}
	if childSnap == nil {
		t.Fatal("no child span")
	}
	row, err := spanToStepRow(childSnap)
	if err != nil {
		t.Fatal(err)
	}
	if row.ParentSpanID == "" {
		t.Error("parent_span_id should be populated for child spans")
	}
	if row.Name != "child" {
		t.Errorf("name: got %q", row.Name)
	}
}

func int64FromAny(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64: // JSON-decoded integers come back as float64
		return int64(x)
	default:
		return 0
	}
}

func TestSumLLMTotals(t *testing.T) {
	mk := func(attrs map[string]any) stepRow {
		j, err := json.Marshal(attrs)
		if err != nil {
			t.Fatal(err)
		}
		return stepRow{Attributes: j}
	}
	llmStep := func(in, out, cached int64, costUSD float64) stepRow {
		return mk(map[string]any{
			"llm.tokens_input":  in,
			"llm.tokens_output": out,
			"llm.tokens_cached": cached,
			"llm.cost_usd":      costUSD,
		})
	}

	tests := []struct {
		name  string
		steps []stepRow
		want  llmTotals
	}{
		{
			name: "empty",
		},
		{
			name:  "single LLM step",
			steps: []stepRow{llmStep(100, 50, 10, 0.0012)},
			want:  llmTotals{TokensIn: 100, TokensOut: 50, TokensCached: 10, CostUSD: 0.0012},
		},
		{
			name: "LLM + non-LLM",
			steps: []stepRow{
				llmStep(200, 75, 0, 0.0010),
				mk(map[string]any{"http.status": int64(200)}), // ignored
			},
			want: llmTotals{TokensIn: 200, TokensOut: 75, CostUSD: 0.0010},
		},
		{
			name: "multiple LLM steps are additive",
			steps: []stepRow{
				llmStep(100, 50, 10, 0.0012),
				llmStep(200, 75, 0, 0.0010),
				llmStep(50, 25, 5, 0.0003),
			},
			want: llmTotals{TokensIn: 350, TokensOut: 150, TokensCached: 15, CostUSD: 0.0025},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sumLLMTotals(tc.steps)
			if got.TokensIn != tc.want.TokensIn {
				t.Errorf("TokensIn = %d, want %d", got.TokensIn, tc.want.TokensIn)
			}
			if got.TokensOut != tc.want.TokensOut {
				t.Errorf("TokensOut = %d, want %d", got.TokensOut, tc.want.TokensOut)
			}
			if got.TokensCached != tc.want.TokensCached {
				t.Errorf("TokensCached = %d, want %d", got.TokensCached, tc.want.TokensCached)
			}
			diff := got.CostUSD - tc.want.CostUSD
			if diff < -1e-9 || diff > 1e-9 {
				t.Errorf("CostUSD = %.10f, want %.10f", got.CostUSD, tc.want.CostUSD)
			}
		})
	}
}
