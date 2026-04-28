package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"darek/internal/testutil/llmstub"
	"darek/llm"
	"darek/tools"

	"github.com/stretchr/testify/require"
)

type stubTool struct {
	name string
	out  string
}

func (s stubTool) Name() string                { return s.name }
func (s stubTool) Description() string         { return "stub" }
func (s stubTool) JSONSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return s.out, nil
}

func TestAgent_NoToolCalls_ReturnsAnswerInOneIter(t *testing.T) {
	srv := llmstub.New(t, llmstub.Reply{Body: assistantReply("hello there", nil)})
	cl, err := llm.New(llm.Options{APIKey: "k", BaseURL: srv.URL, Model: "gpt-4.1"})
	require.NoError(t, err)
	reg, _ := tools.NewRegistry(time.Second)
	a, err := New(Options{LLM: cl, Tools: reg, MaxIterations: 5})
	require.NoError(t, err)

	res, err := a.RunTurn(context.Background(), "hi")
	require.NoError(t, err)
	require.Equal(t, "hello there", res.Output)
	require.Equal(t, 1, res.Iterations)
}

func TestAgent_ToolCallThenFinal(t *testing.T) {
	srv := llmstub.New(t,
		llmstub.Reply{Body: assistantReply("", []toolCall{{ID: "c1", Name: "echo", Args: `{"q":"x"}`}})},
		llmstub.Reply{Body: assistantReply("done", nil)},
	)
	cl, err := llm.New(llm.Options{APIKey: "k", BaseURL: srv.URL, Model: "gpt-4.1"})
	require.NoError(t, err)
	reg, _ := tools.NewRegistry(time.Second)
	require.NoError(t, reg.Register(stubTool{name: "echo", out: "echoed"}))

	a, _ := New(Options{LLM: cl, Tools: reg, MaxIterations: 5})
	res, err := a.RunTurn(context.Background(), "do it")
	require.NoError(t, err)
	require.Equal(t, "done", res.Output)
	require.Equal(t, 2, res.Iterations)
}

func TestAgent_HitsMaxIterations(t *testing.T) {
	// Always returns a tool call → never converges.
	loop := llmstub.Reply{Body: assistantReply("", []toolCall{{ID: "c1", Name: "echo", Args: `{}`}})}
	srv := llmstub.New(t, loop, loop, loop)
	cl, _ := llm.New(llm.Options{APIKey: "k", BaseURL: srv.URL, Model: "gpt-4.1"})
	reg, _ := tools.NewRegistry(time.Second)
	require.NoError(t, reg.Register(stubTool{name: "echo", out: "x"}))

	a, _ := New(Options{LLM: cl, Tools: reg, MaxIterations: 2})
	_, err := a.RunTurn(context.Background(), "loop")
	require.Error(t, err)
	require.Contains(t, err.Error(), "max iterations")
}

// --- helpers ---

type toolCall struct {
	ID, Name, Args string
}

func assistantReply(content string, calls []toolCall) map[string]interface{} {
	msg := map[string]interface{}{"role": "assistant", "content": content}
	if len(calls) > 0 {
		tc := make([]map[string]interface{}, 0, len(calls))
		for _, c := range calls {
			tc = append(tc, map[string]interface{}{
				"id":   c.ID,
				"type": "function",
				"function": map[string]interface{}{
					"name":      c.Name,
					"arguments": c.Args,
				},
			})
		}
		msg["tool_calls"] = tc
		msg["content"] = ""
	}
	finishReason := "stop"
	if len(calls) > 0 {
		finishReason = "tool_calls"
	}
	return map[string]interface{}{
		"id":      "c-1",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   "gpt-4.1",
		"choices": []map[string]interface{}{{
			"index": 0, "message": msg, "finish_reason": finishReason,
		}},
		"usage": map[string]interface{}{
			"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
			"prompt_tokens_details": map[string]interface{}{"cached_tokens": 0},
		},
	}
}
