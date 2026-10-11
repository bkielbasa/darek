package memory

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

type fakeChat struct {
	gotUser string
	resp    string
	err     error
}

func (f *fakeChat) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	for _, m := range p.Messages {
		if m.OfUser != nil && m.OfUser.Content.OfString.Value != "" {
			f.gotUser = m.OfUser.Content.OfString.Value
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return &openai.ChatCompletion{Choices: []openai.ChatCompletionChoice{
		{Message: openai.ChatCompletionMessage{Content: f.resp}},
	}}, nil
}

func extractMsg(id, body string, at time.Time) whatsapp.Message {
	return whatsapp.Message{ID: id, GroupJID: "g1@g.us", SenderJID: "x", SenderName: "Bart", Kind: "text", Body: body, SentAt: at}
}

func TestExtract_HappyPath(t *testing.T) {
	chat := &fakeChat{resp: `{"memories":[
		{"kind":"agreement","summary":"Dinner with Marek on Sunday","people":["Marek"],"due_at":"2026-10-18T19:00:00+02:00","dedupe_key":"dinner-marek-sunday"},
		{"kind":"person_fact","summary":"Anna's son Kuba plays football","people":["Anna"],"due_at":null,"dedupe_key":"kuba-football"}
	]}`}
	ex := NewExtractor(chat)

	at := time.Date(2026, 10, 11, 9, 0, 0, 0, time.Local)
	cands, err := ex.Extract(context.Background(), "Family", []whatsapp.Message{extractMsg("a", "let's do dinner Sunday", at)})
	require.NoError(t, err)
	require.Len(t, cands, 2)
	require.Equal(t, "agreement", cands[0].Kind)
	require.NotNil(t, cands[0].DueAt)
	require.Equal(t, "person_fact", cands[1].Kind)
	require.Nil(t, cands[1].DueAt)
	require.Contains(t, chat.gotUser, "Group: Family")
	require.Contains(t, chat.gotUser, "Today is 2026-10-11")
}

func TestExtract_FencedJSON(t *testing.T) {
	chat := &fakeChat{resp: "```json\n{\"memories\":[{\"kind\":\"event\",\"summary\":\"x\",\"dedupe_key\":\"x\"}]}\n```"}
	ex := NewExtractor(chat)
	cands, err := ex.Extract(context.Background(), "G", []whatsapp.Message{extractMsg("a", "x", time.Now())})
	require.NoError(t, err)
	require.Len(t, cands, 1)
}

func TestExtract_InvalidJSON(t *testing.T) {
	ex := NewExtractor(&fakeChat{resp: "not json"})
	_, err := ex.Extract(context.Background(), "G", []whatsapp.Message{extractMsg("a", "x", time.Now())})
	require.Error(t, err)
}

func TestExtract_EmptyResponse(t *testing.T) {
	ex := NewExtractor(&fakeChat{resp: "   "})
	_, err := ex.Extract(context.Background(), "G", []whatsapp.Message{extractMsg("a", "x", time.Now())})
	require.Error(t, err)
}

func TestExtract_LLMError(t *testing.T) {
	ex := NewExtractor(&fakeChat{err: errors.New("boom")})
	_, err := ex.Extract(context.Background(), "G", []whatsapp.Message{extractMsg("a", "x", time.Now())})
	require.Error(t, err)
}

func TestExtract_NoMemories(t *testing.T) {
	ex := NewExtractor(&fakeChat{resp: `{"memories":[]}`})
	cands, err := ex.Extract(context.Background(), "G", []whatsapp.Message{extractMsg("a", "hi", time.Now())})
	require.NoError(t, err)
	require.Empty(t, cands)
}

func TestParseCandidates_Coercion(t *testing.T) {
	cands, err := parseCandidates(`{"memories":[
		{"kind":"weird","summary":"keep me","dedupe_key":""},
		{"kind":"event","summary":"  "}
	]}`)
	require.NoError(t, err)
	require.Len(t, cands, 1, "empty summary dropped")
	require.Equal(t, "other", cands[0].Kind, "unknown kind coerced")
	require.Equal(t, "other-keep-me", dedupeKey(cands[0]), "dedupe key derived from kind+summary")
}

func TestParseDueAt(t *testing.T) {
	require.Nil(t, parseDueAt(nil))
	empty := "  "
	require.Nil(t, parseDueAt(&empty))
	garbage := "sometime next week"
	require.Nil(t, parseDueAt(&garbage))

	rfc := "2026-10-18T19:00:00+02:00"
	got := parseDueAt(&rfc)
	require.NotNil(t, got)
	require.Equal(t, time.Date(2026, 10, 18, 17, 0, 0, 0, time.UTC), *got)

	date := "2026-10-18"
	got = parseDueAt(&date)
	require.NotNil(t, got)
	require.Equal(t, 2026, got.In(time.Local).Year())
}

func TestExtract_TruncatesToTail(t *testing.T) {
	chat := &fakeChat{resp: `{"memories":[]}`}
	ex := NewExtractor(chat)
	now := time.Now()
	var msgs []whatsapp.Message
	for i := 0; i < 200; i++ {
		msgs = append(msgs, extractMsg(string(rune('a'+i%26)), strings.Repeat("a", 80), now.Add(time.Duration(i)*time.Minute)))
	}
	_, err := ex.Extract(context.Background(), "Big", msgs)
	require.NoError(t, err)
	require.Contains(t, chat.gotUser, msgs[len(msgs)-1].Body)
}

func TestSlug(t *testing.T) {
	require.Equal(t, "dinner-marek-sunday", slug("Dinner  Marek, Sunday!"))
	require.Equal(t, "a-b", slug("--a--b--"))
	require.Equal(t, "", slug("!!!   "))
}
