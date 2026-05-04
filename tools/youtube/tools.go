package youtube

import (
	"context"
	"encoding/json"
	"fmt"

	"darek/tools"
)

// transcriptSchema is the JSON-schema describing youtube.transcript args.
const transcriptSchema = `{
	"type":"object",
	"properties":{
		"url":{"type":"string","description":"YouTube video URL (watch?v=, youtu.be/, shorts/, embed/)."},
		"lang":{"type":"string","description":"Optional ISO language code (e.g. 'en', 'es'). Defaults to manual English, then auto English, then first available track."}
	},
	"required":["url"],
	"additionalProperties":false
}`

// fetcher is the subset of *Client used by Transcript; lets tests inject a fake.
type fetcher interface {
	Fetch(ctx context.Context, rawURL, lang string) (Result, error)
}

type Transcript struct {
	client fetcher
}

func NewTranscript(client *Client) *Transcript {
	return &Transcript{client: client}
}

// Compile-time check.
var _ tools.Tool = (*Transcript)(nil)

func (*Transcript) Name() string { return "youtube.transcript" }

func (*Transcript) Description() string {
	return "Fetch the transcript of a YouTube video as plain text. Returns title, channel, duration, and transcript text."
}

func (*Transcript) JSONSchema() json.RawMessage {
	return json.RawMessage(transcriptSchema)
}

func (t *Transcript) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL  string `json:"url"`
		Lang string `json:"lang,omitempty"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	if in.URL == "" {
		return "", fmt.Errorf("url is required")
	}
	res, err := t.client.Fetch(ctx, in.URL, in.Lang)
	if err != nil {
		return "", err
	}
	return formatResult(res), nil
}

func formatResult(r Result) string {
	return fmt.Sprintf("Title: %s\nChannel: %s\nDuration: %s\n\n%s",
		r.Title, r.Channel, formatDuration(r.Duration), r.Text)
}
