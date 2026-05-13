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
	ID                uuid.UUID
	TraceID           string
	SpanID            string
	Kind              string
	Name              string
	StartedAt         time.Time
	EndedAt           time.Time
	DurationMS        int64
	Status            string
	Error             string
	Attributes        []byte // JSON-encoded
	TotalTokensIn     int64
	TotalTokensOut    int64
	TotalTokensCached int64
	TotalCostUSD      float64
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

func spanToExecutionRow(s sdktrace.ReadOnlySpan, totals llmTotals) (executionRow, error) {
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
		ID:                uuid.New(),
		TraceID:           s.SpanContext().TraceID().String(),
		SpanID:            s.SpanContext().SpanID().String(),
		Kind:              kind,
		Name:              s.Name(),
		StartedAt:         s.StartTime(),
		EndedAt:           s.EndTime(),
		DurationMS:        durationMS(s),
		Status:            status,
		Error:             errMsg,
		Attributes:        attrsJSON,
		TotalTokensIn:     totals.TokensIn,
		TotalTokensOut:    totals.TokensOut,
		TotalTokensCached: totals.TokensCached,
		TotalCostUSD:      totals.CostUSD,
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

// llmTotals aggregates LLM-usage counts across the buffered step rows of a
// single execution. Populated by sumLLMTotals at flush time and stored in
// the executions row's new columns.
type llmTotals struct {
	TokensIn     int64
	TokensOut    int64
	TokensCached int64
	CostUSD      float64
}

// sumLLMTotals walks step rows for one execution and sums the llm.* numeric
// attributes. Rows without those attributes contribute zero. Pure: no DB,
// no clock — given equal inputs it returns equal outputs.
func sumLLMTotals(steps []stepRow) llmTotals {
	var out llmTotals
	for _, s := range steps {
		if len(s.Attributes) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(s.Attributes, &m); err != nil {
			continue
		}
		out.TokensIn += attrInt64(m, "llm.tokens_input")
		out.TokensOut += attrInt64(m, "llm.tokens_output")
		out.TokensCached += attrInt64(m, "llm.tokens_cached")
		out.CostUSD += attrFloat64(m, "llm.cost_usd")
	}
	return out
}

// attrInt64 reads a numeric attribute from an unmarshaled JSON map. Numbers
// come back as float64 from encoding/json by default; we accept either kind
// defensively in case a caller constructs the map with typed integers.
func attrInt64(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func attrFloat64(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	default:
		return 0
	}
}
