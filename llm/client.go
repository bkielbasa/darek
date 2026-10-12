package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"darek/obs"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type Client struct {
	c       openai.Client
	model   string
	timeout time.Duration
	m       *obs.Metrics
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
	}, nil
}

// Chat is the only method the agent uses. The agent passes raw OpenAI types so
// it owns prompting, tool-call parsing, and message-history shaping.
func (cl *Client) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	ctx, cancel := context.WithTimeout(ctx, cl.timeout)
	defer cancel()

	params.Model = cl.model
	start := time.Now()
	var resp *openai.ChatCompletion
	depErr := obs.Dep(ctx, "openai_chat", "chat", func(ctx context.Context) error {
		var err error
		resp, err = cl.c.Chat.Completions.New(ctx, params)
		if err != nil {
			return err
		}
		// Attach token + cost numbers to the span while it's still open
		// (obs.Dep ends the span when this fn returns). The same numbers
		// are recorded as metrics below.
		in := int(resp.Usage.PromptTokens)
		out := int(resp.Usage.CompletionTokens)
		cached := int(resp.Usage.PromptTokensDetails.CachedTokens)
		cost := Cost(cl.model, in, out, cached)
		trace.SpanFromContext(ctx).SetAttributes(
			attribute.String("llm.model", cl.model),
			attribute.Int64("llm.tokens_input", int64(in)),
			attribute.Int64("llm.tokens_output", int64(out)),
			attribute.Int64("llm.tokens_cached", int64(cached)),
			attribute.Float64("llm.cost_usd", cost),
		)
		return nil
	})
	dur := time.Since(start).Seconds()

	outcome := "ok"
	if depErr != nil {
		outcome = "error"
	}
	cl.m.LLMLatency.Record(ctx, dur,
		metric.WithAttributes(attribute.String("model", cl.model), attribute.String("outcome", outcome)),
	)
	if depErr != nil {
		return nil, fmt.Errorf("openai chat: %w", depErr)
	}

	in := int(resp.Usage.PromptTokens)
	out := int(resp.Usage.CompletionTokens)
	cached := int(resp.Usage.PromptTokensDetails.CachedTokens)
	cost := Cost(cl.model, in, out, cached)

	mAttr := metric.WithAttributes(attribute.String("model", cl.model))
	cl.m.TokensInput.Add(ctx, int64(in), mAttr)
	cl.m.TokensOutput.Add(ctx, int64(out), mAttr)
	cl.m.TokensCached.Add(ctx, int64(cached), mAttr)
	cl.m.LLMCostUSD.Add(ctx, cost, mAttr)
	return resp, nil
}

func (cl *Client) Model() string { return cl.model }

// Embed returns one embedding vector per input, in the same order. model is an
// embedding model (e.g. text-embedding-3-small) and is independent of the chat
// model configured on the client.
func (cl *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float64, error) {
	if model == "" {
		return nil, errors.New("embedding model required")
	}
	if len(inputs) == 0 {
		return nil, errors.New("no inputs to embed")
	}

	ctx, cancel := context.WithTimeout(ctx, cl.timeout)
	defer cancel()

	start := time.Now()
	var resp *openai.CreateEmbeddingResponse
	depErr := obs.Dep(ctx, "openai_embed", "embed", func(ctx context.Context) error {
		var err error
		resp, err = cl.c.Embeddings.New(ctx, openai.EmbeddingNewParams{
			Model: model,
			Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: inputs},
		})
		return err
	})
	dur := time.Since(start).Seconds()

	outcome := "ok"
	if depErr != nil {
		outcome = "error"
	}
	cl.m.LLMLatency.Record(ctx, dur,
		metric.WithAttributes(attribute.String("model", model), attribute.String("outcome", outcome)),
	)
	if depErr != nil {
		return nil, fmt.Errorf("openai embed: %w", depErr)
	}

	tokenAttr := metric.WithAttributes(attribute.String("model", model))
	cl.m.TokensInput.Add(ctx, resp.Usage.PromptTokens, tokenAttr)

	out := make([][]float64, len(inputs))
	for _, e := range resp.Data {
		i := int(e.Index)
		if i < 0 || i >= len(out) {
			return nil, fmt.Errorf("embed: index %d out of range", e.Index)
		}
		out[i] = e.Embedding
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("embed: missing vector for input %d", i)
		}
	}
	return out, nil
}

