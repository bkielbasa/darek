package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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

// Section is one row of the WhatsApp digest section: the group's name, the
// LLM summary, plus minimal metadata for the rendered email.
type Section struct {
	GroupName    string
	Summary      string
	MessageCount int
	FirstSentAt  time.Time
	LastSentAt   time.Time
}

// RenderText renders sections as plain text. Empty input → "".
func RenderText(sections []Section) string {
	if len(sections) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("WhatsApp — last 24h\n")
	b.WriteString(strings.Repeat("─", 19))
	b.WriteString("\n\n")
	for i, s := range sections {
		fmt.Fprintf(&b, "▸ %s (%d messages, %s)\n",
			s.GroupName, s.MessageCount, formatTimeRange(s.FirstSentAt, s.LastSentAt))
		for _, line := range wrapText(s.Summary, 75) {
			fmt.Fprintf(&b, "   %s\n", line)
		}
		if i < len(sections)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// RenderHTML renders sections as inline-styled HTML safe for email clients.
// Empty input → "".
func RenderHTML(sections []Section) string {
	if len(sections) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<section class="wa-digest" style="margin-top:1.5rem;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;color:#1c1c1c;">`)
	b.WriteString(`<h2 style="margin:0 0 .75rem;font-size:1.05rem;font-weight:600;">WhatsApp</h2>`)
	for _, s := range sections {
		b.WriteString(`<div style="background:#fff;border:1px solid #e8e3d8;border-radius:6px;padding:.75rem 1rem;margin-bottom:.5rem;">`)
		fmt.Fprintf(&b, `<div style="margin-bottom:.35rem;"><strong>%s</strong> <span style="color:#6b6b6b;font-size:.9em;"> · %d messages · %s</span></div>`,
			htmlEscape(s.GroupName), s.MessageCount, htmlEscape(formatTimeRange(s.FirstSentAt, s.LastSentAt)))
		fmt.Fprintf(&b, `<div style="line-height:1.45;">%s</div>`, htmlEscape(s.Summary))
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section>`)
	return b.String()
}

// formatTimeRange shows "14:02–22:48" if both ends are on the same day,
// otherwise "Mon 14:02 – Tue 22:48".
func formatTimeRange(from, to time.Time) string {
	from, to = from.Local(), to.Local()
	if sameDay(from, to) {
		return fmt.Sprintf("%s–%s", from.Format("15:04"), to.Format("15:04"))
	}
	return fmt.Sprintf("%s %s – %s %s",
		from.Format("Mon"), from.Format("15:04"),
		to.Format("Mon"), to.Format("15:04"))
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// wrapText word-wraps s at width, returning lines (no trailing newline).
// Naive: splits on whitespace, no hyphenation, no smart fitting.
func wrapText(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{s}
	}
	var lines []string
	var cur strings.Builder
	for _, w := range words {
		if cur.Len() == 0 {
			cur.WriteString(w)
			continue
		}
		if cur.Len()+1+len(w) > width {
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(w)
			continue
		}
		cur.WriteByte(' ')
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// htmlEscape replaces the four characters that affect HTML parsing.
// We don't use html/template here because we want the surrounding wrapper
// HTML in our format string and only the user-supplied bits escaped.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}
