package recommend

import (
	"context"
	"errors"
	"testing"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
)

type fakeSelectorChat struct {
	content string
	err     error
	calls   int
}

func (f *fakeSelectorChat) Chat(_ context.Context, _ openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &openai.ChatCompletion{
		Choices: []openai.ChatCompletionChoice{
			{Message: openai.ChatCompletionMessage{Content: f.content}},
		},
	}, nil
}

func cands(n int) []Candidate {
	out := make([]Candidate, n)
	for i := range out {
		out[i] = Candidate{Title: "cand", Kind: "article"}
	}
	return out
}

func TestSelect_HappyPath(t *testing.T) {
	fc := &fakeSelectorChat{content: `{"recommendations":[{"index":1,"reason":"matches your taste"},{"index":2,"reason":"new angle"}]}`}
	sel := NewSelector(fc)

	got, err := sel.Select(context.Background(), Profile{}, cands(3), 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "matches your taste", got[0].Reason)
	require.Equal(t, "new angle", got[1].Reason)
}

func TestSelect_FencedJSON(t *testing.T) {
	fc := &fakeSelectorChat{content: "```json\n{\"recommendations\":[{\"index\":0,\"reason\":\"r\"}]}\n```"}
	got, err := NewSelector(fc).Select(context.Background(), Profile{}, cands(2), 3)
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestSelect_EmptyCandidatesNoCall(t *testing.T) {
	fc := &fakeSelectorChat{}
	got, err := NewSelector(fc).Select(context.Background(), Profile{}, nil, 3)
	require.NoError(t, err)
	require.Nil(t, got)
	require.Zero(t, fc.calls, "model must not be called with no candidates")
}

func TestSelect_InvalidJSON(t *testing.T) {
	fc := &fakeSelectorChat{content: "nope"}
	_, err := NewSelector(fc).Select(context.Background(), Profile{}, cands(2), 3)
	require.Error(t, err)
}

func TestSelect_ChatError(t *testing.T) {
	fc := &fakeSelectorChat{err: errors.New("boom")}
	_, err := NewSelector(fc).Select(context.Background(), Profile{}, cands(2), 3)
	require.Error(t, err)
}

func TestParsePicks_DropsOutOfRangeAndDupes(t *testing.T) {
	content := `{"recommendations":[
		{"index":5,"reason":"oob"},
		{"index":0,"reason":"first"},
		{"index":0,"reason":"dupe"},
		{"index":1,"reason":"second"}
	]}`
	got, err := parsePicks(content, cands(2), 5)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "first", got[0].Reason)
	require.Equal(t, "second", got[1].Reason)
}

func TestParsePicks_CapsAtCount(t *testing.T) {
	content := `{"recommendations":[{"index":0},{"index":1},{"index":2}]}`
	got, err := parsePicks(content, cands(3), 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
}
