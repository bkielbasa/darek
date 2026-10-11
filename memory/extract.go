package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"darek/tools/whatsapp"

	"github.com/openai/openai-go"
)

// Chat is the subset of *llm.Client used by Extractor. Same pattern as
// whatsapp.Summarizer so tests can inject a fake.
type Chat interface {
	Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

// Extractor turns a group transcript into durable memory candidates.
type Extractor struct{ llm Chat }

func NewExtractor(c Chat) *Extractor { return &Extractor{llm: c} }

const maxMemoryTranscriptChars = 6000

const memorySystemPrompt = `You extract durable memories from a WhatsApp group conversation. ` +
	`Return ONLY a JSON object of the form {"memories":[...]}. Each memory: ` +
	`"kind" is one of "agreement" (something the participants agreed or committed to), ` +
	`"event" (a dated plan/occurrence), "person_fact" (a durable fact about a person), ` +
	`or "other". "summary" is one plain sentence in the language of the conversation. ` +
	`"details" is optional supporting context. "people" is the names of people the ` +
	`memory is about. "due_at" is an ISO-8601 datetime (with offset) for a concrete ` +
	`time, or "YYYY-MM-DD" for a date-only plan, or null. "dedupe_key" is a short ` +
	`stable slug identifying the plan/fact (e.g. "dinner-marek-sunday"); reuse the same ` +
	`key when the conversation mentions the same thing again. Only record things that ` +
	`are explicitly stated — never infer, speculate, or record pleasantries. ` +
	`If there is nothing durable, return {"memories":[]}.`

// Extract returns the durable memories stated in msgs (ascending by SentAt).
func (e *Extractor) Extract(ctx context.Context, groupName string, msgs []whatsapp.Message) ([]Candidate, error) {
	if len(msgs) == 0 {
		return nil, errors.New("extract: no messages")
	}
	user := buildMemoryUserMessage(groupName, msgs)

	resp, err := e.llm.Chat(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(memorySystemPrompt),
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
		return nil, errors.New("extract: empty choices")
	}
	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	if content == "" {
		return nil, errors.New("extract: empty model response")
	}
	return parseCandidates(content)
}

func buildMemoryUserMessage(groupName string, msgs []whatsapp.Message) string {
	last := msgs[len(msgs)-1].SentAt.Local()
	header := fmt.Sprintf("Today is %s (%s), timezone %s.",
		last.Format("2006-01-02"), last.Format("Monday"), last.Format("MST"))

	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "[%s] %s: %s\n",
			m.SentAt.Local().Format("2006-01-02 15:04"), m.SenderName, m.Body)
	}
	transcript := b.String()
	if len(transcript) > maxMemoryTranscriptChars {
		transcript = transcript[len(transcript)-maxMemoryTranscriptChars:]
	}
	return fmt.Sprintf("%s\n\nGroup: %s\n\n%s", header, groupName, transcript)
}

// parseCandidates tolerates fenced JSON and coerces/drops malformed entries.
func parseCandidates(content string) ([]Candidate, error) {
	content = stripCodeFence(content)
	var payload struct {
		Memories []struct {
			Kind      string   `json:"kind"`
			Summary   string   `json:"summary"`
			Details   string   `json:"details"`
			People    []string `json:"people"`
			DueAt     *string  `json:"due_at"`
			DedupeKey string   `json:"dedupe_key"`
		} `json:"memories"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil, fmt.Errorf("parse memories json: %w", err)
	}
	var out []Candidate
	for _, m := range payload.Memories {
		summary := strings.TrimSpace(m.Summary)
		if summary == "" {
			continue
		}
		kind := strings.TrimSpace(m.Kind)
		switch kind {
		case "agreement", "event", "person_fact", "other":
		default:
			kind = "other"
		}
		out = append(out, Candidate{
			Kind:      kind,
			Summary:   summary,
			Details:   strings.TrimSpace(m.Details),
			People:    m.People,
			DueAt:     parseDueAt(m.DueAt),
			DedupeKey: strings.TrimSpace(m.DedupeKey),
		})
	}
	return out, nil
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimPrefix(s, "json")
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

// parseDueAt accepts RFC3339 (with offset) or YYYY-MM-DD (local midnight).
// Unparseable input → nil (a memory without a date is still useful).
func parseDueAt(s *string) *time.Time {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	v := strings.TrimSpace(*s)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		u := t.UTC()
		return &u
	}
	if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		u := t.UTC()
		return &u
	}
	return nil
}
