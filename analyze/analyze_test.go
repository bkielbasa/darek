package analyze_test

import (
	"context"
	"errors"
	"testing"

	"darek/analyze"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
)

// fakeChat returns a canned ChatCompletion content string.
type fakeChat struct {
	content string
	err     error
	gotMsgs []openai.ChatCompletionMessageParamUnion
}

func (f *fakeChat) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	f.gotMsgs = p.Messages
	if f.err != nil {
		return nil, f.err
	}
	return &openai.ChatCompletion{
		Choices: []openai.ChatCompletionChoice{
			{Message: openai.ChatCompletionMessage{Content: f.content}},
		},
	}, nil
}

func TestAnalyze_HappyPath(t *testing.T) {
	fc := &fakeChat{content: `{"summary":"It's a thing.","tags":["go","concurrency","performance"]}`}
	a := analyze.New(fc)

	out, err := a.Analyze(context.Background(), analyze.Input{
		Title: "A Thing",
		URL:   "https://example.com/a",
		Body:  "some body",
	})
	require.NoError(t, err)
	require.Equal(t, "It's a thing.", out.Summary)
	require.Equal(t, []string{"go", "concurrency", "performance"}, out.Tags)

	// Verify the prompt was constructed with title/url/body.
	require.GreaterOrEqual(t, len(fc.gotMsgs), 2, "system + user message expected")
}

func TestAnalyze_TagNormalization(t *testing.T) {
	fc := &fakeChat{content: `{"summary":"x","tags":["Go","go","  CONCURRENCY  ","",""]}`}
	a := analyze.New(fc)
	out, err := a.Analyze(context.Background(), analyze.Input{Title: "t", URL: "u"})
	require.NoError(t, err)
	require.Equal(t, []string{"go", "concurrency"}, out.Tags)
}

func TestAnalyze_TagCapAt7(t *testing.T) {
	fc := &fakeChat{content: `{"summary":"x","tags":["a","b","c","d","e","f","g","h","i"]}`}
	a := analyze.New(fc)
	out, err := a.Analyze(context.Background(), analyze.Input{Title: "t", URL: "u"})
	require.NoError(t, err)
	require.Len(t, out.Tags, 7)
}

func TestAnalyze_MalformedJSONError(t *testing.T) {
	fc := &fakeChat{content: `not json at all`}
	a := analyze.New(fc)
	_, err := a.Analyze(context.Background(), analyze.Input{Title: "t", URL: "u"})
	require.Error(t, err)
}

func TestAnalyze_EmptyBodyStillCallsModel(t *testing.T) {
	fc := &fakeChat{content: `{"summary":"based on title","tags":["x"]}`}
	a := analyze.New(fc)
	out, err := a.Analyze(context.Background(), analyze.Input{Title: "Just a title", URL: "u"})
	require.NoError(t, err)
	require.Equal(t, "based on title", out.Summary)
}

func TestAnalyze_PropagatesChatError(t *testing.T) {
	fc := &fakeChat{err: errors.New("boom")}
	a := analyze.New(fc)
	_, err := a.Analyze(context.Background(), analyze.Input{Title: "t", URL: "u"})
	require.Error(t, err)
}
