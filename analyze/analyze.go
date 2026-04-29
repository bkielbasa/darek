package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
)

// Input is the payload an analyzer receives. Body may be empty (e.g. videos,
// tweets, links with no source-provided text); the prompt instructs the model
// to fall back to the title in that case.
type Input struct {
	Title string
	URL   string
	Body  string
}

// Output is the parsed model result. Summary is plain text, Tags are
// lowercase, deduped, and capped at 7.
type Output struct {
	Summary string
	Tags    []string
}

// Chat is the subset of *llm.Client used by Analyzer. Defined here so tests
// can supply a fake; *llm.Client satisfies it without changes.
type Chat interface {
	Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

// Analyzer wraps a Chat with a fixed prompt for link summarization + tagging.
type Analyzer struct {
	llm Chat
}

// New constructs an Analyzer.
func New(c Chat) *Analyzer { return &Analyzer{llm: c} }

const systemPrompt = `You are summarizing links the user is considering reading. Reply with strict JSON only:
{"summary": "...", "tags": ["...", "..."]}
The summary is 1-3 plain-text sentences, factual, no marketing language. Tags are 3-7 lowercase short topical labels (single word or hyphenated bigram). Do not invent facts. If the body is empty or unrelated to the title, summarize from the title alone.`

const maxBodyChars = 6000

// Analyze sends the input to the LLM and returns the parsed Output. The body
// is truncated to maxBodyChars before sending.
func (a *Analyzer) Analyze(ctx context.Context, in Input) (Output, error) {
	body := in.Body
	if len(body) > maxBodyChars {
		body = body[:maxBodyChars]
	}
	user := fmt.Sprintf("Title: %s\nURL: %s\nBody:\n%s", in.Title, in.URL, body)

	resp, err := a.llm.Chat(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(user),
		},
	})
	if err != nil {
		return Output{}, fmt.Errorf("chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return Output{}, fmt.Errorf("analyze: empty choices")
	}

	content := resp.Choices[0].Message.Content
	var raw struct {
		Summary string   `json:"summary"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return Output{}, fmt.Errorf("analyze: bad json from model: %w", err)
	}
	return Output{
		Summary: strings.TrimSpace(raw.Summary),
		Tags:    normalizeTags(raw.Tags),
	}, nil
}

// normalizeTags lowercases, trims, drops blanks, dedupes, and caps at 7.
func normalizeTags(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
		if len(out) >= 7 {
			break
		}
	}
	return out
}
