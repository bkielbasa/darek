package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"darek/exechistory"
	"darek/llm"
	"darek/obs"
	"darek/tools"

	"github.com/openai/openai-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type Agent struct {
	llm      *llm.Client
	tools    *tools.Registry
	maxIters int
	tracer   trace.Tracer
	m        *obs.Metrics
}

type Options struct {
	LLM           *llm.Client
	Tools         *tools.Registry
	MaxIterations int
}

func New(opt Options) (*Agent, error) {
	if opt.LLM == nil || opt.Tools == nil {
		return nil, errors.New("llm and tools required")
	}
	if opt.MaxIterations <= 0 {
		opt.MaxIterations = 10
	}
	m, err := obs.MetricsInstance()
	if err != nil {
		return nil, err
	}
	return &Agent{
		llm:      opt.LLM,
		tools:    opt.Tools,
		maxIters: opt.MaxIterations,
		tracer:   otel.Tracer("darek/agent"),
		m:        m,
	}, nil
}

type TurnResult struct {
	Output     string
	Iterations int
}

func (a *Agent) RunTurn(ctx context.Context, userInput string) (*TurnResult, error) {
	ctx, span := a.tracer.Start(ctx, "darek.turn",
		trace.WithAttributes(attribute.Int("user_input_chars", len(userInput))),
	)
	exechistory.MarkExecution(span, "chat-turn")
	defer span.End()
	start := time.Now()
	outcome := "ok"
	defer func() {
		a.m.TurnDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("outcome", outcome)))
	}()

	system := BuildSystemPrompt(time.Now(), a.tools.Names())
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(system),
		openai.UserMessage(userInput),
	}
	toolDefs, nameMap := buildToolParams(a.tools.OpenAIToolDefs())

	var (
		final string
		iters int
	)
	for iters = 0; iters < a.maxIters; iters++ {
		params := openai.ChatCompletionNewParams{
			Messages: msgs,
			Tools:    toolDefs,
		}
		resp, err := a.llm.Chat(ctx, params)
		if err != nil {
			outcome = "error"
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, fmt.Errorf("llm: %w", err)
		}
		choice := resp.Choices[0]
		msg := choice.Message
		if len(msg.ToolCalls) == 0 {
			final = msg.Content
			break
		}
		// ToParam() converts the response message back to a ChatCompletionMessageParamUnion
		// that includes the tool_calls list — required so the model sees its own tool invocations.
		msgs = append(msgs, msg.ToParam())
		for _, tc := range msg.ToolCalls {
			origName := nameMap[tc.Function.Name]
			if origName == "" {
				origName = tc.Function.Name
			}
			result, err := a.tools.Execute(ctx, origName, json.RawMessage(tc.Function.Arguments))
			payload := result
			if err != nil {
				payload = fmt.Sprintf("error: %s", err.Error())
			}
			// ToolMessage signature: content first, toolCallID second.
			msgs = append(msgs, openai.ToolMessage(payload, tc.ID))
		}
	}

	if iters == a.maxIters {
		outcome = "error"
		a.m.AgentMaxItersHit.Add(ctx, 1)
		err := fmt.Errorf("hit max iterations (%d) without final answer", a.maxIters)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	a.m.TurnIters.Record(ctx, int64(iters+1))
	span.SetAttributes(attribute.Int("iterations", iters+1))
	return &TurnResult{Output: final, Iterations: iters + 1}, nil
}

// buildToolParams converts the registry's generic tool defs into typed OpenAI params.
// Tool names are sanitized to match OpenAI's required pattern (^[a-zA-Z0-9_-]+$); a
// reverse map of sanitized→original is returned so the loop can dispatch tool calls
// back to the registry by their canonical name.
func buildToolParams(defs []map[string]any) ([]openai.ChatCompletionToolParam, map[string]string) {
	out := make([]openai.ChatCompletionToolParam, 0, len(defs))
	nameMap := make(map[string]string, len(defs))
	for _, d := range defs {
		fn := d["function"].(map[string]any)
		orig := fn["name"].(string)
		sanitized := sanitizeToolName(orig)
		nameMap[sanitized] = orig
		params := openai.FunctionDefinitionParam{
			Name:        sanitized,
			Description: openai.String(fn["description"].(string)),
			Parameters:  openai.FunctionParameters(fn["parameters"].(map[string]any)),
		}
		out = append(out, openai.ChatCompletionToolParam{
			Function: params,
		})
	}
	return out, nameMap
}

// sanitizeToolName replaces any character outside [a-zA-Z0-9_-] with '_' so the
// name satisfies OpenAI's tools[].function.name pattern.
func sanitizeToolName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
