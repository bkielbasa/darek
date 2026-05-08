package blogmarketing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"darek/blogmarketing"
	"darek/tools/blogfeed"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
)

type fakeChat struct {
	resp    string
	respErr error
	gotMsgs []openai.ChatCompletionMessageParamUnion
}

func (f *fakeChat) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	f.gotMsgs = p.Messages
	if f.respErr != nil {
		return nil, f.respErr
	}
	return &openai.ChatCompletion{
		Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{Content: f.resp},
		}},
	}, nil
}

func TestDrafterOpenAI_HappyPath(t *testing.T) {
	chat := &fakeChat{
		resp: `{
			"x":        {"launch":"X-l","reshare":"X-r","resurface":"X-rs"},
			"mastodon": {"launch":"M-l","reshare":"M-r","resurface":"M-rs"},
			"linkedin": {"launch":"L-l","reshare":"L-r","resurface":"L-rs"}
		}`,
	}
	d := blogmarketing.NewOpenAIDrafter(chat)
	drafts, err := d.Draft(context.Background(), blogfeed.Entry{
		URL:         "https://example.com/post",
		Title:       "Hello",
		Summary:     "World",
		PublishedAt: time.Now(),
	})
	require.NoError(t, err)
	require.Equal(t, "X-l", drafts[blogmarketing.PlatformX][blogmarketing.CadenceLaunch])
	require.Equal(t, "M-r", drafts[blogmarketing.PlatformMastodon][blogmarketing.CadenceReshare2W])
	require.Equal(t, "L-rs", drafts[blogmarketing.PlatformLinkedIn][blogmarketing.CadenceResurface3Mo])
	require.NotEmpty(t, chat.gotMsgs)
}

func TestDrafterOpenAI_ChatError(t *testing.T) {
	chat := &fakeChat{respErr: errors.New("boom")}
	d := blogmarketing.NewOpenAIDrafter(chat)
	_, err := d.Draft(context.Background(), blogfeed.Entry{URL: "x", Title: "t"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
}

func TestDrafterOpenAI_BadJSON(t *testing.T) {
	chat := &fakeChat{resp: "not json"}
	d := blogmarketing.NewOpenAIDrafter(chat)
	_, err := d.Draft(context.Background(), blogfeed.Entry{URL: "x", Title: "t"})
	require.Error(t, err)
}

func TestDrafterOpenAI_MissingCell(t *testing.T) {
	chat := &fakeChat{resp: `{"x":{"launch":"only"}, "mastodon":{}, "linkedin":{}}`}
	d := blogmarketing.NewOpenAIDrafter(chat)
	_, err := d.Draft(context.Background(), blogfeed.Entry{URL: "x", Title: "t"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing")
}
