package exechistory

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// scopePrefix is the tracer name prefix that distinguishes spans we want
// to persist. Auto-spans from otelpgx / otelhttp use other scopes.
const scopePrefix = "darek/"

// attributeDenyPrefixes lists attribute key prefixes that originate from
// auto-instrumentation (otelpgx, database drivers, low-level net stack)
// and would just clutter the JSON column. Anything not matching one of
// these prefixes is kept so application-domain attributes (e.g.
// freshrss.imported, todoist.tasks) survive without an explicit allowlist.
var attributeDenyPrefixes = []string{
	"db.",
	"net.",
	"server.",
	"client.",
	"url.",
	"network.",
}

// executionKind returns the value of the darek.execution.kind attribute
// (whether the span was started with it or set later via SetAttributes).
func executionKind(s sdktrace.ReadOnlySpan) (string, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == KindAttribute {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

type executionRow struct {
	ID         uuid.UUID
	TraceID    string
	SpanID     string
	Kind       string
	Name       string
	StartedAt  time.Time
	EndedAt    time.Time
	DurationMS int64
	Status     string
	Error      string
	Attributes []byte // JSON-encoded
}

type stepRow struct {
	ID           uuid.UUID
	ParentSpanID string
	SpanID       string
	Name         string
	StartedAt    time.Time
	EndedAt      time.Time
	DurationMS   int64
	Status       string
	Error        string
	Attributes   []byte // JSON-encoded
	Events       []byte // JSON-encoded
}

func spanToExecutionRow(s sdktrace.ReadOnlySpan) (executionRow, error) {
	kind, _ := executionKind(s)
	attrs := filterAttributes(s.Attributes())
	attrsJSON, err := json.Marshal(attrs)
	if err != nil {
		return executionRow{}, err
	}
	status := "ok"
	errMsg := ""
	if s.Status().Code == codes.Error {
		status = "error"
		errMsg = s.Status().Description
	}
	return executionRow{
		ID:         uuid.New(),
		TraceID:    s.SpanContext().TraceID().String(),
		SpanID:     s.SpanContext().SpanID().String(),
		Kind:       kind,
		Name:       s.Name(),
		StartedAt:  s.StartTime(),
		EndedAt:    s.EndTime(),
		DurationMS: durationMS(s),
		Status:     status,
		Error:      errMsg,
		Attributes: attrsJSON,
	}, nil
}

func spanToStepRow(s sdktrace.ReadOnlySpan) (stepRow, error) {
	attrs := filterAttributes(s.Attributes())
	attrsJSON, err := json.Marshal(attrs)
	if err != nil {
		return stepRow{}, err
	}
	events := make([]SpanEvent, 0, len(s.Events()))
	for _, e := range s.Events() {
		events = append(events, SpanEvent{
			Time:       e.Time,
			Name:       e.Name,
			Attributes: kvSliceToMap(e.Attributes),
		})
	}
	eventsJSON, err := json.Marshal(events)
	if err != nil {
		return stepRow{}, err
	}
	status := "ok"
	errMsg := ""
	if s.Status().Code == codes.Error {
		status = "error"
		errMsg = s.Status().Description
	}
	parent := ""
	if pc := s.Parent(); pc.IsValid() {
		parent = pc.SpanID().String()
	}
	return stepRow{
		ID:           uuid.New(),
		ParentSpanID: parent,
		SpanID:       s.SpanContext().SpanID().String(),
		Name:         s.Name(),
		StartedAt:    s.StartTime(),
		EndedAt:      s.EndTime(),
		DurationMS:   durationMS(s),
		Status:       status,
		Error:        errMsg,
		Attributes:   attrsJSON,
		Events:       eventsJSON,
	}, nil
}

func durationMS(s sdktrace.ReadOnlySpan) int64 {
	d := s.EndTime().Sub(s.StartTime())
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}

func filterAttributes(attrs []attribute.KeyValue) map[string]any {
	out := make(map[string]any, len(attrs))
	for _, kv := range attrs {
		k := string(kv.Key)
		if k == KindAttribute {
			continue
		}
		if isDeniedAttribute(k) {
			continue
		}
		out[k] = attributeValue(kv.Value)
	}
	return out
}

func isDeniedAttribute(key string) bool {
	for _, p := range attributeDenyPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func kvSliceToMap(attrs []attribute.KeyValue) map[string]any {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]any, len(attrs))
	for _, kv := range attrs {
		out[string(kv.Key)] = attributeValue(kv.Value)
	}
	return out
}

func attributeValue(v attribute.Value) any {
	switch v.Type() {
	case attribute.BOOL:
		return v.AsBool()
	case attribute.INT64:
		return v.AsInt64()
	case attribute.FLOAT64:
		return v.AsFloat64()
	case attribute.STRING:
		return v.AsString()
	case attribute.BOOLSLICE:
		return v.AsBoolSlice()
	case attribute.INT64SLICE:
		return v.AsInt64Slice()
	case attribute.FLOAT64SLICE:
		return v.AsFloat64Slice()
	case attribute.STRINGSLICE:
		return v.AsStringSlice()
	default:
		return v.Emit()
	}
}
