package exechistory

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// MarkExecution sets the darek.execution.kind attribute on span so the
// Recorder treats it as an execution-root span (and persists it as a row
// in `executions`). Call this immediately after tracer.Start.
//
// Safe to call when no Recorder is registered; the attribute is then just
// a label in Jaeger.
func MarkExecution(span trace.Span, kind string) {
	span.SetAttributes(attribute.String(KindAttribute, kind))
}
