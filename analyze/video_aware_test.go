package analyze_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"darek/analyze"
	"darek/tools/youtube"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
)

// vaTranscriber records the URL it was asked for and returns a fixed result.
type vaTranscriber struct {
	res    youtube.Result
	err    error
	called int
	gotURL string
}

func (f *vaTranscriber) Fetch(ctx context.Context, rawURL, lang string) (youtube.Result, error) {
	f.called++
	f.gotURL = rawURL
	return f.res, f.err
}

// vaChat captures the user message body via the params Messages slice.
// Inspecting message content uses the openai-go union accessor pattern; if the
// API version differs, fall back to scanning the JSON-marshaled params.
type vaChat struct {
	gotUserContent string
	resp           string
	err            error
}

func (f *vaChat) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	for _, msg := range params.Messages {
		// openai-go: each ChatCompletionMessageParamUnion has typed sub-fields.
		// We try OfUser.Content; the Content union itself wraps a string in
		// the simplest case.
		if msg.OfUser != nil {
			// Content is a union; the simple-string variant is .OfString.
			if msg.OfUser.Content.OfString.Value != "" {
				f.gotUserContent = msg.OfUser.Content.OfString.Value
			}
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return &openai.ChatCompletion{
		Choices: []openai.ChatCompletionChoice{
			{Message: openai.ChatCompletionMessage{Content: f.resp}},
		},
	}, nil
}

func TestVideoAware_YouTubeURL_UsesTranscript(t *testing.T) {
	tr := &vaTranscriber{res: youtube.Result{Text: "TRANSCRIPT BODY"}}
	chat := &vaChat{resp: `{"summary":"s","tags":["a","b"]}`}
	v := analyze.NewVideoAware(analyze.New(chat), tr)

	out, err := v.Analyze(context.Background(), analyze.Input{
		Title: "vid",
		URL:   "https://www.youtube.com/watch?v=abcDEF12345",
		Body:  "ignored YT description",
	})
	require.NoError(t, err)
	require.Equal(t, "s", out.Summary)
	require.Equal(t, 1, tr.called)
	require.Equal(t, "https://www.youtube.com/watch?v=abcDEF12345", tr.gotURL)
	require.Contains(t, chat.gotUserContent, "TRANSCRIPT BODY")
	require.NotContains(t, chat.gotUserContent, "ignored YT description")
}

func TestVideoAware_NonYouTubeURL_PassesThrough(t *testing.T) {
	tr := &vaTranscriber{}
	chat := &vaChat{resp: `{"summary":"s","tags":[]}`}
	v := analyze.NewVideoAware(analyze.New(chat), tr)

	_, err := v.Analyze(context.Background(), analyze.Input{
		Title: "art",
		URL:   "https://example.com/an-article",
		Body:  "article body",
	})
	require.NoError(t, err)
	require.Equal(t, 0, tr.called, "transcriber must not be called for non-YouTube URLs")
	require.Contains(t, chat.gotUserContent, "article body")
}

func TestVideoAware_TranscriptFetchError(t *testing.T) {
	tr := &vaTranscriber{err: errors.New("no captions available")}
	chat := &vaChat{resp: `{"summary":"s","tags":[]}`}
	v := analyze.NewVideoAware(analyze.New(chat), tr)

	_, err := v.Analyze(context.Background(), analyze.Input{
		Title: "vid",
		URL:   "https://www.youtube.com/watch?v=abcDEF12345",
	})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "youtube transcript"), err)
	require.True(t, strings.Contains(err.Error(), "no captions available"), err)
	require.Equal(t, "", chat.gotUserContent, "chat must not be called when transcript fetch fails")
}
