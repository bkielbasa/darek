package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeFetcher struct {
	res     Result
	err     error
	gotURL  string
	gotLang string
}

func (f *fakeFetcher) Fetch(ctx context.Context, rawURL, lang string) (Result, error) {
	f.gotURL = rawURL
	f.gotLang = lang
	return f.res, f.err
}

func TestTranscript_NameDescriptionSchema(t *testing.T) {
	tr := &Transcript{}
	require.Equal(t, "youtube.transcript", tr.Name())
	require.NotEmpty(t, tr.Description())

	var schema map[string]any
	require.NoError(t, json.Unmarshal(tr.JSONSchema(), &schema))
	required, _ := schema["required"].([]any)
	require.Contains(t, required, "url")
}

func TestTranscript_Execute_Happy(t *testing.T) {
	f := &fakeFetcher{res: Result{
		Title:    "T",
		Channel:  "C",
		Duration: 73 * time.Second,
		Text:     "the body",
	}}
	tr := &Transcript{client: f}

	out, err := tr.Execute(context.Background(), json.RawMessage(`{"url":"https://youtu.be/abcDEF12345","lang":"es"}`))
	require.NoError(t, err)
	require.Equal(t, "https://youtu.be/abcDEF12345", f.gotURL)
	require.Equal(t, "es", f.gotLang)
	require.Equal(t, "Title: T\nChannel: C\nDuration: 1m 13s\n\nthe body", out)
}

func TestTranscript_Execute_MissingURL(t *testing.T) {
	tr := &Transcript{client: &fakeFetcher{}}
	_, err := tr.Execute(context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "url is required")
}

func TestTranscript_Execute_BadJSON(t *testing.T) {
	tr := &Transcript{client: &fakeFetcher{}}
	_, err := tr.Execute(context.Background(), json.RawMessage(`{not json`))
	require.Error(t, err)
}

func TestTranscript_Execute_FetchError(t *testing.T) {
	f := &fakeFetcher{err: errors.New("no captions available")}
	tr := &Transcript{client: f}
	_, err := tr.Execute(context.Background(), json.RawMessage(`{"url":"https://youtu.be/abcDEF12345"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "no captions available")
}
