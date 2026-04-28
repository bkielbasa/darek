package llm

import (
	"context"
	"testing"

	"darek/internal/testutil/llmstub"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
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
