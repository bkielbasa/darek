package whatsapp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"darek/tools/whatsapp"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
)

// fakeSummaryChat captures the user message body it sees and returns a fixed
// response. Mirrors the analyze-package test pattern.
type fakeSummaryChat struct {
	gotUserContent string
	resp           string
	err            error
}

func (f *fakeSummaryChat) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	for _, msg := range p.Messages {
		if msg.OfUser != nil && msg.OfUser.Content.OfString.Value != "" {
			f.gotUserContent = msg.OfUser.Content.OfString.Value
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

func msgAt(id, sender, body string, sentAt time.Time) whatsapp.Message {
	return whatsapp.Message{
		ID: id, GroupJID: "g1@g.us",
		SenderJID: sender + "@s.whatsapp.net", SenderName: sender,
		Kind: "text", Body: body, SentAt: sentAt,
	}
}

func TestSummarize_HappyPath(t *testing.T) {
	chat := &fakeSummaryChat{resp: "Quick recap of the chat."}
	s := whatsapp.NewSummarizer(chat)

	t1 := time.Date(2026, 5, 4, 14, 23, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Minute)
	msgs := []whatsapp.Message{
		msgAt("a", "Bart", "did anyone see the link?", t1),
		msgAt("b", "Asia", "yes, looks great", t2),
	}

	out, err := s.Summarize(context.Background(), "Family", msgs)
	require.NoError(t, err)
	require.Equal(t, "Quick recap of the chat.", out)
	require.Contains(t, chat.gotUserContent, "Group: Family")
	require.Contains(t, chat.gotUserContent, "Bart: did anyone see the link?")
	require.Contains(t, chat.gotUserContent, "Asia: yes, looks great")
}

func TestSummarize_EmptyMessagesIsError(t *testing.T) {
	s := whatsapp.NewSummarizer(&fakeSummaryChat{})
	_, err := s.Summarize(context.Background(), "G", nil)
	require.Error(t, err)
}

func TestSummarize_EmptyModelResponseIsError(t *testing.T) {
	chat := &fakeSummaryChat{resp: "   "}
	s := whatsapp.NewSummarizer(chat)
	msgs := []whatsapp.Message{msgAt("a", "x", "y", time.Now())}
	_, err := s.Summarize(context.Background(), "G", msgs)
	require.Error(t, err)
}

func TestSummarize_LLMErrorPropagates(t *testing.T) {
	chat := &fakeSummaryChat{err: errors.New("boom")}
	s := whatsapp.NewSummarizer(chat)
	msgs := []whatsapp.Message{msgAt("a", "x", "y", time.Now())}
	_, err := s.Summarize(context.Background(), "G", msgs)
	require.Error(t, err)
}

func TestSummarize_TruncatesLongTranscriptToTail(t *testing.T) {
	chat := &fakeSummaryChat{resp: "ok"}
	s := whatsapp.NewSummarizer(chat)

	// Build a transcript that's well over 6000 chars.
	now := time.Now()
	var msgs []whatsapp.Message
	for i := 0; i < 200; i++ {
		body := strings.Repeat("a", 80) // 80 chars × 200 = 16000+ chars
		msgs = append(msgs, msgAt(string(rune('a'+i%26)), "S", body, now.Add(time.Duration(i)*time.Minute)))
	}

	_, err := s.Summarize(context.Background(), "Big", msgs)
	require.NoError(t, err)
	// User content includes Group prefix + truncated tail. Total length is bounded.
	require.LessOrEqual(t, len(chat.gotUserContent), len("Group: Big\n\n")+6000)
	// Tail-bias: the LAST message bytes must be in there; the FIRST may not be.
	require.Contains(t, chat.gotUserContent, msgs[len(msgs)-1].Body)
}

func TestRenderText_TwoSections(t *testing.T) {
	t1a := time.Date(2026, 5, 5, 9, 11, 0, 0, time.UTC)
	t1b := time.Date(2026, 5, 5, 17, 33, 0, 0, time.UTC)
	t2a := time.Date(2026, 5, 5, 14, 2, 0, 0, time.UTC)
	t2b := time.Date(2026, 5, 5, 22, 48, 0, 0, time.UTC)
	sections := []whatsapp.Section{
		{GroupName: "Family", Summary: "Anna shared photos.", MessageCount: 12, FirstSentAt: t2a, LastSentAt: t2b},
		{GroupName: "Work", Summary: "Discussed migration.", MessageCount: 47, FirstSentAt: t1a, LastSentAt: t1b},
	}

	got := whatsapp.RenderText(sections)
	require.Contains(t, got, "WhatsApp — last 24h")
	require.Contains(t, got, "▸ Family (12 messages,")
	require.Contains(t, got, "▸ Work (47 messages,")
	require.Contains(t, got, "Anna shared photos.")
	require.Contains(t, got, "Discussed migration.")
}

func TestRenderText_EmptyInputIsEmpty(t *testing.T) {
	require.Equal(t, "", whatsapp.RenderText(nil))
	require.Equal(t, "", whatsapp.RenderText([]whatsapp.Section{}))
}

func TestRenderHTML_TwoSections(t *testing.T) {
	sections := []whatsapp.Section{
		{GroupName: "Family", Summary: "Anna shared photos.", MessageCount: 12,
			FirstSentAt: time.Date(2026, 5, 5, 14, 2, 0, 0, time.UTC),
			LastSentAt:  time.Date(2026, 5, 5, 22, 48, 0, 0, time.UTC)},
	}
	got := whatsapp.RenderHTML(sections)
	require.Contains(t, got, `<h2`)
	require.Contains(t, got, `WhatsApp`)
	require.Contains(t, got, `<strong>Family</strong>`)
	require.Contains(t, got, `Anna shared photos.`)
}

func TestRenderHTML_EscapesHostileGroupName(t *testing.T) {
	sections := []whatsapp.Section{
		{GroupName: `<script>alert(1)</script>`, Summary: "x", MessageCount: 1,
			FirstSentAt: time.Now(), LastSentAt: time.Now()},
	}
	got := whatsapp.RenderHTML(sections)
	require.NotContains(t, got, "<script>")
	require.Contains(t, got, "&lt;script&gt;")
}

func TestRenderHTML_EmptyInputIsEmpty(t *testing.T) {
	require.Equal(t, "", whatsapp.RenderHTML(nil))
}
