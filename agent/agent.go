package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	defer span.End()
	start := time.Now()

	system := BuildSystemPrompt(time.Now(), a.tools.Names())
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(system),
		openai.UserMessage(userInput),
	}
	toolDefs := buildToolParams(a.tools.OpenAIToolDefs())

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
			result, err := a.tools.Execute(ctx, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			payload := result
			if err != nil {
				payload = fmt.Sprintf("error: %s", err.Error())
			}
			// ToolMessage signature: content first, toolCallID second.
			msgs = append(msgs, openai.ToolMessage(payload, tc.ID))
		}
	}

	if iters == a.maxIters {
		err := fmt.Errorf("hit max iterations (%d) without final answer", a.maxIters)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	dur := time.Since(start).Seconds()
	a.m.TurnDuration.Record(ctx, dur, metric.WithAttributes(attribute.String("outcome", "ok")))
	a.m.TurnIters.Record(ctx, int64(iters+1))
	span.SetAttributes(attribute.Int("iterations", iters+1))
	return &TurnResult{Output: final, Iterations: iters + 1}, nil
}

// buildToolParams converts the registry's generic tool defs into typed OpenAI params.
// In v1.12.0:
//   - FunctionDefinitionParam.Name is a plain string (not param.Opt).
//   - FunctionDefinitionParam.Description is param.Opt[string], constructed via openai.String().
//   - FunctionDefinitionParam.Parameters is openai.FunctionParameters (= map[string]any).
//   - ChatCompletionToolParam.Type is constant.Function; it serialises to "function" automatically
//     when left as zero value, so no explicit assignment is needed.
func buildToolParams(defs []map[string]any) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(defs))
	for _, d := range defs {
		fn := d["function"].(map[string]any)
		params := openai.FunctionDefinitionParam{
			Name:        fn["name"].(string),
			Description: openai.String(fn["description"].(string)),
			Parameters:  openai.FunctionParameters(fn["parameters"].(map[string]any)),
		}
		out = append(out, openai.ChatCompletionToolParam{
			Function: params,
		})
	}
	return out
}
