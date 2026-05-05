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
