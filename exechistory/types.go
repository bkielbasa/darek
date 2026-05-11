// Package exechistory persists selected OpenTelemetry spans to Postgres so
// the web UI can browse a history of executions (background syncs, manual
// triggers, CLI subcommands, chat turns) and their child steps.
//
// Spans flow to Jaeger via the existing OTLP exporter unchanged; this
// package adds a parallel SpanProcessor that writes execution-root spans
// and their darek/* descendants into the executions and execution_steps
// tables defined by migration 0009.
package exechistory

import (
	"time"

	"github.com/google/uuid"
)

// KindAttribute is the OTel attribute key that marks an execution-root span.
// Spans carrying this attribute become rows in `executions`; spans without
// it (but with a tracer name beginning "darek/") become rows in
// `execution_steps` of the enclosing trace.
const KindAttribute = "darek.execution.kind"

// Execution is the row type returned by Store.List / Store.Get.
type Execution struct {
	ID         uuid.UUID
	TraceID    string
	SpanID     string
	Kind       string
	Name       string
	StartedAt  time.Time
	EndedAt    time.Time
	DurationMS int64
	Status     string // "ok" | "error"
	Error      string
	Attributes map[string]any
}

// Step is the row type for a single execution_steps row.
type Step struct {
	ID           uuid.UUID
	ExecutionID  uuid.UUID
	ParentSpanID string
	SpanID       string
	Name         string
	StartedAt    time.Time
	EndedAt      time.Time
	DurationMS   int64
	Status       string
	Error        string
	Attributes   map[string]any
	Events       []SpanEvent
}

// SpanEvent mirrors the trace.Event payload we serialize into execution_steps.events.
type SpanEvent struct {
	Time       time.Time      `json:"time"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes,omitempty"`
}
