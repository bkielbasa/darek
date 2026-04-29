package obs

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

var depTracer = otel.Tracer("darek/dep")

// Dep wraps an external call: starts a span, runs fn, records dep.requests and
// dep.latency with uniform labels. Use this instead of tracer.Start for any
// call that crosses a network or process boundary.
//
// dep is a fixed enum (e.g. openai_chat, todoist, postgres). op is a per-dep
// enum. Never put user input, URLs, IDs, or error strings in either label.
func Dep(ctx context.Context, dep, op string, fn func(context.Context) error) error {
	if dep == "" {
		return fmt.Errorf("obs.Dep: dep is required")
	}
	if op == "" {
		return fmt.Errorf("obs.Dep: op is required")
	}
	m, err := MetricsInstance()
	if err != nil {
		return fmt.Errorf("obs.Dep metrics: %w", err)
	}
	ctx, span := depTracer.Start(ctx, dep+"."+op,
		trace.WithAttributes(
			attribute.String("dep", dep),
			attribute.String("op", op),
		),
	)
	defer span.End()

	start := time.Now()
	err = fn(ctx)
	dur := time.Since(start).Seconds()

	outcome := "ok"
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	attrs := metric.WithAttributes(
		attribute.String("dep", dep),
		attribute.String("op", op),
		attribute.String("outcome", outcome),
	)
	m.DepRequests.Add(ctx, 1, attrs)
	m.DepLatency.Record(ctx, dur, attrs)
	return err
}
