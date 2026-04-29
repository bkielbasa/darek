package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"darek/obs"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const MaxResultChars = 20_000

var ErrUnknownTool = errors.New("unknown tool")

type Tool interface {
	Name() string
	Description() string
	JSONSchema() json.RawMessage
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

type Registry struct {
	mu      sync.RWMutex
	byName  map[string]Tool
	tracer  trace.Tracer
	m       *obs.Metrics
	timeout time.Duration
}

func NewRegistry(toolTimeout time.Duration) (*Registry, error) {
	m, err := obs.MetricsInstance()
	if err != nil {
		return nil, err
	}
	return &Registry{
		byName:  map[string]Tool{},
		tracer:  otel.Tracer("darek/tools"),
		m:       m,
		timeout: toolTimeout,
	}, nil
}

func (r *Registry) Register(t Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byName[t.Name()]; ok {
		return fmt.Errorf("tool %q already registered", t.Name())
	}
	r.byName[t.Name()] = t
	return nil
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	return out
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byName[name]
	return t, ok
}

// Execute looks up `name`, runs it with timeout, OTEL span, and metrics.
// On tool error, returns the error (caller decides whether to surface to the model).
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	ctx, span := r.tracer.Start(ctx, "tool.execute",
		trace.WithAttributes(
			attribute.String("tool.name", name),
			attribute.Int("tool.args_chars", len(args)),
		),
	)
	defer span.End()

	start := time.Now()
	res, err := t.Execute(ctx, args)
	dur := time.Since(start).Seconds()

	outcome := "ok"
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	if len(res) > MaxResultChars {
		res = res[:MaxResultChars] + "\n\n[truncated by darek; original was longer]"
	}
	span.SetAttributes(attribute.Int("tool.result_chars", len(res)))

	attrs := metric.WithAttributes(
		attribute.String("tool", name),
		attribute.String("outcome", outcome),
	)
	r.m.ToolCalls.Add(ctx, 1, attrs)
	r.m.ToolLatency.Record(ctx, dur, metric.WithAttributes(attribute.String("tool", name)))
	return res, err
}

// OpenAIToolDefs returns the tool list in the shape OpenAI Chat Completions wants.
// We keep the raw shape as map[string]any so this package doesn't import openai-go.
func (r *Registry) OpenAIToolDefs() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]map[string]any, 0, len(r.byName))
	for _, t := range r.byName {
		var schema any
		_ = json.Unmarshal(t.JSONSchema(), &schema)
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name(),
				"description": t.Description(),
				"parameters":  schema,
			},
		})
	}
	return out
}
