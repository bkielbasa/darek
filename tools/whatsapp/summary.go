package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
)

// Chat is the subset of *llm.Client used by Summarizer. Defined here so tests
// can supply a fake; *llm.Client satisfies it without changes.
type Chat interface {
	Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

// Summarizer wraps a Chat with a fixed prompt for per-group WhatsApp summary.
type Summarizer struct {
	llm Chat
}

// NewSummarizer constructs a Summarizer.
func NewSummarizer(c Chat) *Summarizer { return &Summarizer{llm: c} }

const summarySystemPrompt = `You are summarizing a WhatsApp group conversation. Reply with 1-3 plain-text sentences capturing key topics, decisions, and any plans or events. Do not invent facts. If the conversation is mostly pleasantries, say so briefly. Do not include a "Summary:" prefix.`

const maxTranscriptChars = 6000

// Summarize sends the group's recent messages to the LLM and returns a short
// summary. msgs are expected sorted ascending by SentAt.
func (s *Summarizer) Summarize(ctx context.Context, groupName string, msgs []Message) (string, error) {
	if len(msgs) == 0 {
		return "", errors.New("summarize: no messages")
	}
	user := buildSummaryUserMessage(groupName, msgs)

	resp, err := s.llm.Chat(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(summarySystemPrompt),
			openai.UserMessage(user),
		},
	})
	if err != nil {
		return "", fmt.Errorf("chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("summarize: empty choices")
	}
	out := strings.TrimSpace(resp.Choices[0].Message.Content)
	if out == "" {
		return "", errors.New("summarize: empty model response")
	}
	return out, nil
}

// buildSummaryUserMessage formats group + transcript for the LLM. The
// transcript is truncated to the most recent maxTranscriptChars characters
// (newest tail wins) so very chatty groups still fit the prompt budget.
func buildSummaryUserMessage(groupName string, msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "[%s] %s: %s\n",
			m.SentAt.Local().Format("2006-01-02 15:04"),
			m.SenderName,
			m.Body)
	}
	transcript := b.String()
	if len(transcript) > maxTranscriptChars {
		transcript = transcript[len(transcript)-maxTranscriptChars:]
	}
	return fmt.Sprintf("Group: %s\n\n%s", groupName, transcript)
}
