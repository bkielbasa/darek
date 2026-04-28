package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"darek/obs"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type Client struct {
	c       openai.Client
	model   string
	timeout time.Duration
	m       *obs.Metrics
	tracer  trace.Tracer
}

type Options struct {
	APIKey  string
	BaseURL string // optional
	Model   string
	Timeout time.Duration
}

func New(opt Options) (*Client, error) {
	if opt.APIKey == "" {
		return nil, errors.New("api key required")
	}
	if opt.Model == "" {
		return nil, errors.New("model required")
	}
	if opt.Timeout == 0 {
		opt.Timeout = 60 * time.Second
	}
	clientOpts := []option.RequestOption{option.WithAPIKey(opt.APIKey)}
	if opt.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(opt.BaseURL))
	}
	m, err := obs.MetricsInstance()
	if err != nil {
		return nil, fmt.Errorf("metrics: %w", err)
	}
	return &Client{
		c:       openai.NewClient(clientOpts...),
		model:   opt.Model,
		timeout: opt.Timeout,
		m:       m,
		tracer:  otel.Tracer("darek/llm"),
	}, nil
}

// Chat is the only method the agent uses. The agent passes raw OpenAI types so
// it owns prompting, tool-call parsing, and message-history shaping.
func (cl *Client) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	ctx, cancel := context.WithTimeout(ctx, cl.timeout)
	defer cancel()

	ctx, span := cl.tracer.Start(ctx, "chat",
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.system", "openai"),
			attribute.String("gen_ai.request.model", cl.model),
		),
	)
	defer span.End()

	start := time.Now()
	params.Model = cl.model
	resp, err := cl.c.Chat.Completions.New(ctx, params)
	dur := time.Since(start).Seconds()

	outcome := "ok"
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	cl.m.LLMLatency.Record(ctx, dur,
		metric.WithAttributes(attribute.String("model", cl.model), attribute.String("outcome", outcome)),
	)
	if err != nil {
		return nil, fmt.Errorf("openai chat: %w", err)
	}

	in := int(resp.Usage.PromptTokens)
	out := int(resp.Usage.CompletionTokens)
	cached := int(resp.Usage.PromptTokensDetails.CachedTokens)
	cost := Cost(cl.model, in, out, cached)

	span.SetAttributes(
		attribute.Int("gen_ai.usage.input_tokens", in),
		attribute.Int("gen_ai.usage.output_tokens", out),
		attribute.Int("darek.tokens.cached", cached),
		attribute.Float64("darek.llm.cost_usd", cost),
	)
	mAttr := metric.WithAttributes(attribute.String("model", cl.model))
	cl.m.TokensInput.Add(ctx, int64(in), mAttr)
	cl.m.TokensOutput.Add(ctx, int64(out), mAttr)
	cl.m.TokensCached.Add(ctx, int64(cached), mAttr)
	cl.m.LLMCostUSD.Add(ctx, cost, mAttr)
	return resp, nil
}

func (cl *Client) Model() string { return cl.model }
