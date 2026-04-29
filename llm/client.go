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
		return err
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
