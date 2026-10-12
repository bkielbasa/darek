package recommend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
)

// Chat is the subset of *llm.Client used by Selector, so tests can inject a
// fake.
type Chat interface {
	Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

// Selector re-ranks candidates with an LLM using the taste profile.
type Selector struct {
	llm Chat
}

// NewSelector constructs a Selector.
func NewSelector(c Chat) *Selector { return &Selector{llm: c} }

const selectSystemPrompt = `You are Bartek's reading recommender. Given a taste profile ` +
	`(links he liked and disliked) and a numbered list of unrated candidates, choose up to N ` +
	`links he is most likely to enjoy reading. Reply with strict JSON only: ` +
	`{"recommendations":[{"index":<int>,"reason":"<one short sentence>"}]}. ` +
	`Rules: index refers to the candidate list; never invent links; when good candidates of ` +
	`both kinds exist include at least one article and one video; keep reasons concrete and ` +
	`under 20 words; if nothing is a good fit, return an empty list.`

// Select asks the model to pick up to count candidates. Empty input yields a
// nil result without calling the model.
func (s *Selector) Select(ctx context.Context, p Profile, cands []Candidate, count int) ([]Suggestion, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	if count <= 0 {
		count = 3
	}
	user := buildSelectUserMessage(p, cands)

	resp, err := s.llm.Chat(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(selectSystemPrompt),
			openai.UserMessage(user),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &openai.ResponseFormatJSONObjectParam{},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("select: empty choices")
	}
	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	if content == "" {
		return nil, errors.New("select: empty model response")
	}
	return parsePicks(content, cands, count)
}

func buildSelectUserMessage(p Profile, cands []Candidate) string {
	var b strings.Builder
	b.WriteString("Liked:\n")
	writeExamples(&b, p.Liked)
	b.WriteString("\nDisliked:\n")
	writeExamples(&b, p.Disliked)
	b.WriteString("\nCandidates:\n")
	for i, c := range cands {
		fmt.Fprintf(&b, "[%d] (%s, %s) %s — %s\n", i, c.Kind, c.Feed, c.Title, truncate(c.Summary, 160))
	}
	return b.String()
}

func writeExamples(b *strings.Builder, exs []Example) {
	if len(exs) == 0 {
		b.WriteString("- (none)\n")
		return
	}
	for _, e := range exs {
		fmt.Fprintf(b, "- [%s] %s — %s\n", e.Kind, e.Title, truncate(e.Summary, 160))
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// parsePicks unwraps the model's JSON, drops out-of-range and duplicate
// indexes, caps at count, and attaches the chosen candidates.
func parsePicks(content string, cands []Candidate, count int) ([]Suggestion, error) {
	content = stripFence(content)
	var payload struct {
		Recommendations []struct {
			Index  int    `json:"index"`
			Reason string `json:"reason"`
		} `json:"recommendations"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil, fmt.Errorf("parse recommendations json: %w", err)
	}
	seen := map[int]struct{}{}
	var out []Suggestion
	for _, r := range payload.Recommendations {
		if r.Index < 0 || r.Index >= len(cands) {
			continue
		}
		if _, ok := seen[r.Index]; ok {
			continue
		}
		seen[r.Index] = struct{}{}
		out = append(out, Suggestion{Candidate: cands[r.Index], Reason: strings.TrimSpace(r.Reason)})
		if len(out) >= count {
			break
		}
	}
	return out, nil
}

func stripFence(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") {
		return content
	}
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimPrefix(content, "json")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}
