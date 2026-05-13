package llm

import (
	"context"
	"testing"

	"darek/internal/testutil/llmstub"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestChat_HappyPath(t *testing.T) {
	server := llmstub.New(t, llmstub.Reply{
		Body: map[string]interface{}{
			"id":      "chatcmpl-1",
			"object":  "chat.completion",
			"created": 1700000000,
			"model":   "gpt-4.1",
			"choices": []map[string]interface{}{{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "hello world",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_tokens":      15,
				"prompt_tokens_details": map[string]interface{}{"cached_tokens": 0},
			},
		},
	})

	cl, err := New(Options{
		APIKey:  "test",
		BaseURL: server.URL,
		Model:   "gpt-4.1",
	})
	require.NoError(t, err)

	resp, err := cl.Chat(context.Background(), openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("hi"),
		},
	})
	require.NoError(t, err)
	require.Equal(t, "hello world", resp.Choices[0].Message.Content)
	require.Equal(t, int64(10), resp.Usage.PromptTokens)
}

func TestChat_PropagatesError(t *testing.T) {
	server := llmstub.New(t, llmstub.Reply{
		StatusCode: 500,
		Body:       map[string]interface{}{"error": map[string]interface{}{"message": "boom"}},
	})
	cl, err := New(Options{APIKey: "test", BaseURL: server.URL, Model: "gpt-4.1"})
	require.NoError(t, err)
	_, err = cl.Chat(context.Background(), openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	require.Error(t, err)
}

func TestChat_EmitsLLMSpanAttributes(t *testing.T) {
	// Capture all spans into an in-memory exporter via a TracerProvider
	// set as the global. obs.Dep fetches its tracer from the global
	// provider on first use, so we restore the previous one in cleanup.
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	server := llmstub.New(t, llmstub.Reply{
		Body: map[string]interface{}{
			"id":      "chatcmpl-1",
			"object":  "chat.completion",
			"created": 1700000000,
			"model":   "gpt-4.1",
			"choices": []map[string]interface{}{{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "ok",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{
				"prompt_tokens":     100,
				"completion_tokens": 50,
				"total_tokens":      150,
				"prompt_tokens_details": map[string]interface{}{"cached_tokens": 20},
			},
		},
	})

	cl, err := New(Options{APIKey: "test", BaseURL: server.URL, Model: "gpt-4.1"})
	require.NoError(t, err)
	_, err = cl.Chat(context.Background(), openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	require.NoError(t, err)

	// obs.Dep creates one span named "openai_chat.chat".
	var found bool
	for _, sp := range exp.GetSpans().Snapshots() {
		if sp.Name() != "openai_chat.chat" {
			continue
		}
		found = true
		attrs := map[string]any{}
		for _, kv := range sp.Attributes() {
			switch kv.Value.Type().String() {
			case "INT64":
				attrs[string(kv.Key)] = kv.Value.AsInt64()
			case "FLOAT64":
				attrs[string(kv.Key)] = kv.Value.AsFloat64()
			case "STRING":
				attrs[string(kv.Key)] = kv.Value.AsString()
			}
		}
		require.Equal(t, "gpt-4.1", attrs["llm.model"])
		require.Equal(t, int64(100), attrs["llm.tokens_input"])
		require.Equal(t, int64(50), attrs["llm.tokens_output"])
		require.Equal(t, int64(20), attrs["llm.tokens_cached"])
		require.Greater(t, attrs["llm.cost_usd"].(float64), 0.0,
			"cost should be > 0 for a known model")
	}
	require.True(t, found, "expected an openai_chat.chat span")
}
