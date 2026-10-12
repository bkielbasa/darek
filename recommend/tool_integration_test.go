//go:build integration

package recommend

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
)

type fakeLLM struct {
	content string
}

func (f *fakeLLM) Embed(_ context.Context, _ string, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i := range inputs {
		out[i] = axisVec(i % 4)
	}
	return out, nil
}

func (f *fakeLLM) Chat(_ context.Context, _ openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	return &openai.ChatCompletion{
		Choices: []openai.ChatCompletionChoice{
			{Message: openai.ChatCompletionMessage{Content: f.content}},
		},
	}, nil
}

func TestTool_RecommendsAndRespectsSeen(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	liked := seed(t, ls, "https://e.com/liked", "liked", "article", intp(5))
	require.NoError(t, s.SetEmbedding(ctx, liked, axisVec(0)))
	seed(t, ls, "https://e.com/target", "Kubernetes scheduling explained", "article", nil)

	llm := &fakeLLM{content: `{"recommendations":[{"index":0,"reason":"fits your taste"}]}`}
	tool := NewTool(s, llm, Options{Count: 3, CandidatePool: 10, MinLiked: 1})

	out, err := tool.Execute(ctx, json.RawMessage(`{"count":1}`))
	require.NoError(t, err)
	require.Contains(t, out, "Kubernetes scheduling explained")
	require.Contains(t, out, "fits")

	out, err = tool.Execute(ctx, nil)
	require.NoError(t, err)
	require.Contains(t, out, "no reading suggestions", "already-recommended link not repeated")

	out, err = tool.Execute(ctx, json.RawMessage(`{"include_seen":true}`))
	require.NoError(t, err)
	require.Contains(t, out, "Kubernetes scheduling explained", "include_seen surfaces it again")
}

func TestTool_KindFilter(t *testing.T) {
	s, ls := newRecStore(t)
	ctx := context.Background()

	liked := seed(t, ls, "https://e.com/liked", "liked", "article", intp(5))
	require.NoError(t, s.SetEmbedding(ctx, liked, axisVec(0)))
	art := seed(t, ls, "https://e.com/art", "close article", "article", nil)
	vid := seed(t, ls, "https://e.com/vid", "close video", "video", nil)
	require.NoError(t, s.SetEmbedding(ctx, art, axisVec(0)))
	require.NoError(t, s.SetEmbedding(ctx, vid, axisVec(0)))

	llm := &fakeLLM{content: `{"recommendations":[{"index":0,"reason":"r"}]}`}
	tool := NewTool(s, llm, Options{Count: 3, CandidatePool: 10, MinLiked: 1})

	out, err := tool.Execute(ctx, json.RawMessage(`{"kind":"video"}`))
	require.NoError(t, err)
	require.Contains(t, out, "close video (video)")
	require.NotContains(t, out, "close article")
}
