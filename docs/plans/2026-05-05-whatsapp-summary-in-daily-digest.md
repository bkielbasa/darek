# WhatsApp Summary in Daily Digest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Append a "WhatsApp" section to the existing `darek calendar daily-digest` email containing one short LLM-generated summary per opted-in group, covering messages we have not yet summarized. After the email is sent, mark those messages so they don't repeat tomorrow.

**Architecture:** New `summarized_at` column on `whatsapp_messages` plus a partial index. New `Summarizer` type (`*llm.Client` via a `Chat` interface). New `BuildSummary` orchestrator that pulls opted-in groups, fetches their unsummarized messages from the last 7 days, summarizes each, and returns sections + IDs. New `RenderText` / `RenderHTML` for the section. The cron pod (`darek calendar daily-digest`) wires it in — no whatsmeow connection in the cron path; it only reads Postgres.

**Tech Stack:** Existing Go stack (pgx, openai-go, html/template). No new direct deps. Tests use `github.com/stretchr/testify/require` and the project's pg testcontainer helper.

**Design source:** [docs/specs/2026-05-05-whatsapp-summary-in-daily-digest-design.md](../specs/2026-05-05-whatsapp-summary-in-daily-digest-design.md), approved 2026-05-05.

**Out of scope (deferred):** Read-receipt-aware filtering, excluding own messages, configurable lookback, the chat-side `whatsapp.summarize_group` agent tool, the `whatsapp.send` agent tool.

---

## File Map

| Path | Responsibility |
|---|---|
| `db/migrations/0007_whatsapp_summarized.up.sql` | (create) `summarized_at` column + partial index. |
| `tools/whatsapp/store.go` | (modify) add `OptedInGroups`, `UnsummarizedMessages`, `MarkSummarized`. |
| `tools/whatsapp/store_test.go` | (modify) integration tests for the three new methods. |
| `tools/whatsapp/summary.go` | (create) `Chat` interface, `Summarizer`, `Section`, `BuildSummary`, `RenderText`, `RenderHTML`. |
| `tools/whatsapp/summary_test.go` | (create) unit tests: summarizer with fake Chat + golden tests for renderers. |
| `tools/whatsapp/summary_integration_test.go` | (create) integration tests for `BuildSummary` end-to-end. |
| `cmd/darek/daily_digest.go` | (modify) open db pool when WhatsApp is enabled, build LLM client, call `BuildSummary`, append to email, `MarkSummarized` after send-success. |
| `README.md` | (modify) document the WhatsApp summary section in the daily digest. |

---

## Task 1 — Migration + store methods (TDD, integration)

**Files:**
- Create: `db/migrations/0007_whatsapp_summarized.up.sql`
- Modify: `tools/whatsapp/store.go`
- Modify: `tools/whatsapp/store_test.go`

- [ ] **Step 1: Create the migration**

`db/migrations/0007_whatsapp_summarized.up.sql`:

```sql
ALTER TABLE whatsapp_messages
    ADD COLUMN summarized_at timestamptz;

CREATE INDEX idx_whatsapp_messages_unsummarized
    ON whatsapp_messages (group_jid, sent_at)
    WHERE summarized_at IS NULL;
```

- [ ] **Step 2: Append failing tests**

Append to `tools/whatsapp/store_test.go`:

```go
func TestStore_OptedInGroups(t *testing.T) {
	s, ctx := newTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "G1"))
	require.NoError(t, s.UpsertGroup(ctx, "g2@g.us", "G2"))
	require.NoError(t, s.UpsertGroup(ctx, "g3@g.us", "G3"))
	require.NoError(t, s.SetIngestEnabled(ctx, "g1@g.us", true))
	require.NoError(t, s.SetIngestEnabled(ctx, "g3@g.us", true))

	got, err := s.OptedInGroups(ctx)
	require.NoError(t, err)
	jids := []string{}
	for _, g := range got {
		jids = append(jids, g.JID)
	}
	require.ElementsMatch(t, []string{"g1@g.us", "g3@g.us"}, jids)
}

func TestStore_UnsummarizedMessages_FiltersAndOrder(t *testing.T) {
	s, ctx := newTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "G1"))

	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Second)
	mid := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	rec := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)

	require.NoError(t, s.InsertMessage(ctx, Message{ID: "old", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "x", Kind: "text", Body: "long ago", SentAt: old}))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "mid", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "x", Kind: "text", Body: "earlier", SentAt: mid}))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "rec", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "x", Kind: "text", Body: "recent", SentAt: rec}))

	msgs, err := s.UnsummarizedMessages(ctx, "g1@g.us", 7)
	require.NoError(t, err)
	require.Len(t, msgs, 2, "old message outside 7-day window must be excluded")
	require.Equal(t, "mid", msgs[0].ID, "ASC by sent_at")
	require.Equal(t, "rec", msgs[1].ID)
}

func TestStore_MarkSummarized(t *testing.T) {
	s, ctx := newTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "G1"))
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "m1", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "x", Kind: "text", Body: "a", SentAt: now}))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "m2", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "x", Kind: "text", Body: "b", SentAt: now}))

	require.NoError(t, s.MarkSummarized(ctx, []string{"m1"}))

	left, err := s.UnsummarizedMessages(ctx, "g1@g.us", 7)
	require.NoError(t, err)
	require.Len(t, left, 1)
	require.Equal(t, "m2", left[0].ID)

	// Idempotent: re-marking m1 plus a non-existent ID is a no-op without error.
	require.NoError(t, s.MarkSummarized(ctx, []string{"m1", "missing"}))
	left, err = s.UnsummarizedMessages(ctx, "g1@g.us", 7)
	require.NoError(t, err)
	require.Len(t, left, 1)
}

func TestStore_MarkSummarized_EmptyIsNoop(t *testing.T) {
	s, ctx := newTestStore(t)
	require.NoError(t, s.MarkSummarized(ctx, nil))
	require.NoError(t, s.MarkSummarized(ctx, []string{}))
}
```

- [ ] **Step 3: Run, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test -tags integration ./tools/whatsapp/ -run "TestStore_OptedInGroups|TestStore_UnsummarizedMessages|TestStore_MarkSummarized" -v`
Expected: FAIL — three methods undefined.

- [ ] **Step 4: Implement**

Append to `tools/whatsapp/store.go`:

```go
// OptedInGroups returns groups where ingest_enabled = true, ordered by name.
// Used by BuildSummary as the outer loop.
func (s *Store) OptedInGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.jid, g.name, g.ingest_enabled,
		       COALESCE(c.cnt, 0)         AS msg_count,
		       c.last                      AS last_at
		  FROM whatsapp_groups g
		  LEFT JOIN (
		    SELECT group_jid, count(*) AS cnt, max(sent_at) AS last
		      FROM whatsapp_messages
		     GROUP BY group_jid
		  ) c ON c.group_jid = g.jid
		 WHERE g.ingest_enabled = true
		 ORDER BY g.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.JID, &g.Name, &g.IngestEnabled, &g.MessageCount, &g.LastMessageAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UnsummarizedMessages returns messages for the group where summarized_at IS
// NULL and sent_at >= now() - lookbackDays. Sorted ascending by sent_at.
func (s *Store) UnsummarizedMessages(ctx context.Context, groupJID string, lookbackDays int) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, group_jid, sender_jid, sender_name, kind, body, sent_at
		  FROM whatsapp_messages
		 WHERE group_jid = $1
		   AND summarized_at IS NULL
		   AND sent_at >= now() - make_interval(days => $2)
		 ORDER BY sent_at
	`, groupJID, lookbackDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.GroupJID, &m.SenderJID, &m.SenderName, &m.Kind, &m.Body, &m.SentAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkSummarized sets summarized_at = now() for the given message IDs in one
// statement. Idempotent: messages already summarized or missing IDs are
// silently skipped (the WHERE clause matches nothing for them).
func (s *Store) MarkSummarized(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE whatsapp_messages SET summarized_at = now() WHERE id = ANY($1)`,
		ids)
	return err
}
```

- [ ] **Step 5: Run, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test -tags integration ./tools/whatsapp/ -v`
Expected: all 8 store tests pass (5 existing + 4 new).

- [ ] **Step 6: Commit**

```bash
git add db/migrations/0007_whatsapp_summarized.up.sql tools/whatsapp/store.go tools/whatsapp/store_test.go
git commit -m "feat(whatsapp): summarized_at column + store methods for digest"
```

---

## Task 2 — `Summarizer` (TDD)

**Files:**
- Create: `tools/whatsapp/summary.go`
- Create: `tools/whatsapp/summary_test.go`

- [ ] **Step 1: Create the file with type stubs**

Create `tools/whatsapp/summary.go`:

```go
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
```

- [ ] **Step 2: Write the failing tests**

Create `tools/whatsapp/summary_test.go`:

```go
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
```

Add `darek/tools/whatsapp` to imports if needed; the test file is `package whatsapp_test` (external) so it must use the qualified prefix.

- [ ] **Step 3: Run, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/whatsapp/ -run TestSummarize -v`
Expected: PASS for all 5 tests. (Stub Summarizer was already added in Step 1, so tests compile and exercise it.)

- [ ] **Step 4: Commit**

```bash
git add tools/whatsapp/summary.go tools/whatsapp/summary_test.go
git commit -m "feat(whatsapp): per-group summarizer wrapping llm.Client"
```

---

## Task 3 — `Section` + `RenderText` / `RenderHTML` (TDD)

**Files:**
- Modify: `tools/whatsapp/summary.go`
- Modify: `tools/whatsapp/summary_test.go`

- [ ] **Step 1: Add types + render stubs**

Append to `tools/whatsapp/summary.go`:

```go
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
		// Indent the summary by 3 spaces, naive wrap at 75 cols.
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
```

- [ ] **Step 2: Append render tests**

Append to `tools/whatsapp/summary_test.go`:

```go
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
```

- [ ] **Step 3: Run, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/whatsapp/ -run "TestRender" -v`
Expected: PASS for all 5 tests.

- [ ] **Step 4: Run all unit tests in the package**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/whatsapp/ -v`
Expected: all unit tests pass (decode + pairing + summary + render).

- [ ] **Step 5: Commit**

```bash
git add tools/whatsapp/summary.go tools/whatsapp/summary_test.go
git commit -m "feat(whatsapp): Section type + RenderText/RenderHTML for digest section"
```

---

## Task 4 — `BuildSummary` orchestrator (TDD, integration)

**Files:**
- Modify: `tools/whatsapp/summary.go`
- Create: `tools/whatsapp/summary_integration_test.go`

- [ ] **Step 1: Add the orchestrator**

Append to `tools/whatsapp/summary.go`:

```go
// LookbackDays bounds how far back BuildSummary will look at unsummarized
// messages. Bounded so a long darek-serve outage doesn't dump weeks of
// messages into a single summary, which would be both expensive and useless.
const LookbackDays = 7

// SummarizerInterface is the subset of *Summarizer used by BuildSummary —
// kept narrow so tests can inject a stub.
type SummarizerInterface interface {
	Summarize(ctx context.Context, groupName string, msgs []Message) (string, error)
}

// BuildSummary pulls opted-in groups, fetches their unsummarized messages
// from the last LookbackDays, summarizes each non-empty group, and returns
// the rendered sections plus the message IDs to mark summarized after the
// email is sent.
//
// Failure of one group's summarization is logged via fmt.Fprintf to stderr
// and does not abort the others; the failing group's IDs are not included
// in the returned slice (so tomorrow retries).
func BuildSummary(
	ctx context.Context,
	store *Store,
	summarizer SummarizerInterface,
	logger func(format string, a ...any),
) ([]Section, []string, error) {
	if logger == nil {
		logger = func(string, ...any) {}
	}

	groups, err := store.OptedInGroups(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("opted-in groups: %w", err)
	}

	var sections []Section
	var ids []string

	for _, g := range groups {
		msgs, err := store.UnsummarizedMessages(ctx, g.JID, LookbackDays)
		if err != nil {
			return nil, nil, fmt.Errorf("unsummarized for %s: %w", g.JID, err)
		}
		if len(msgs) == 0 {
			continue
		}

		summary, err := summarizer.Summarize(ctx, g.Name, msgs)
		if err != nil {
			logger("whatsapp summary skipped for %q: %v\n", g.Name, err)
			continue
		}

		sections = append(sections, Section{
			GroupName:    g.Name,
			Summary:      summary,
			MessageCount: len(msgs),
			FirstSentAt:  msgs[0].SentAt,
			LastSentAt:   msgs[len(msgs)-1].SentAt,
		})
		for _, m := range msgs {
			ids = append(ids, m.ID)
		}
	}

	return sections, ids, nil
}
```

- [ ] **Step 2: Write the failing tests**

Create `tools/whatsapp/summary_integration_test.go`:

```go
//go:build integration

package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

// stubSummarizer is the SummarizerInterface fake used by BuildSummary tests.
// It returns a fixed string for the given group OR an error.
type stubSummarizer struct {
	respByGroup map[string]string
	errByGroup  map[string]error
	calls       []string
}

func (s *stubSummarizer) Summarize(ctx context.Context, groupName string, msgs []Message) (string, error) {
	s.calls = append(s.calls, groupName)
	if err, ok := s.errByGroup[groupName]; ok {
		return "", err
	}
	if r, ok := s.respByGroup[groupName]; ok {
		return r, nil
	}
	return "default summary for " + groupName, nil
}

func newSummaryTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	_, raw := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), raw))
	return NewStore(db.Wrap(raw)), context.Background()
}

func TestBuildSummary_HappyPath(t *testing.T) {
	s, ctx := newSummaryTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "Family"))
	require.NoError(t, s.UpsertGroup(ctx, "g2@g.us", "Work"))
	require.NoError(t, s.SetIngestEnabled(ctx, "g1@g.us", true))
	require.NoError(t, s.SetIngestEnabled(ctx, "g2@g.us", true))

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "a", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "Bart", Kind: "text", Body: "hey", SentAt: now.Add(-2 * time.Hour)}))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "b", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "Asia", Kind: "text", Body: "hi", SentAt: now.Add(-1 * time.Hour)}))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "c", GroupJID: "g2@g.us", SenderJID: "x", SenderName: "Karol", Kind: "text", Body: "report?", SentAt: now}))

	stub := &stubSummarizer{
		respByGroup: map[string]string{
			"Family": "Family chat sample.",
			"Work":   "Work chat sample.",
		},
	}
	sections, ids, err := BuildSummary(ctx, s, stub, nil)
	require.NoError(t, err)
	require.Len(t, sections, 2)
	require.ElementsMatch(t, []string{"Family", "Work"}, []string{sections[0].GroupName, sections[1].GroupName})
	require.ElementsMatch(t, []string{"a", "b", "c"}, ids)
}

func TestBuildSummary_SkipsOptedOutGroup(t *testing.T) {
	s, ctx := newSummaryTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "Tracked"))
	require.NoError(t, s.UpsertGroup(ctx, "g2@g.us", "Untracked"))
	require.NoError(t, s.SetIngestEnabled(ctx, "g1@g.us", true))

	now := time.Now().UTC()
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "a", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "S", Kind: "text", Body: "x", SentAt: now}))

	stub := &stubSummarizer{}
	sections, ids, err := BuildSummary(ctx, s, stub, nil)
	require.NoError(t, err)
	require.Len(t, sections, 1)
	require.Equal(t, "Tracked", sections[0].GroupName)
	require.Equal(t, []string{"a"}, ids)
}

func TestBuildSummary_SkipsGroupWithNoUnsummarized(t *testing.T) {
	s, ctx := newSummaryTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "Quiet"))
	require.NoError(t, s.UpsertGroup(ctx, "g2@g.us", "Active"))
	require.NoError(t, s.SetIngestEnabled(ctx, "g1@g.us", true))
	require.NoError(t, s.SetIngestEnabled(ctx, "g2@g.us", true))

	now := time.Now().UTC()
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "a", GroupJID: "g2@g.us", SenderJID: "x", SenderName: "S", Kind: "text", Body: "x", SentAt: now}))

	stub := &stubSummarizer{}
	sections, _, err := BuildSummary(ctx, s, stub, nil)
	require.NoError(t, err)
	require.Len(t, sections, 1)
	require.Equal(t, "Active", sections[0].GroupName)
}

func TestBuildSummary_FailedGroupExcludedFromIDs(t *testing.T) {
	s, ctx := newSummaryTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "Good"))
	require.NoError(t, s.UpsertGroup(ctx, "g2@g.us", "Bad"))
	require.NoError(t, s.SetIngestEnabled(ctx, "g1@g.us", true))
	require.NoError(t, s.SetIngestEnabled(ctx, "g2@g.us", true))

	now := time.Now().UTC()
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "g1m", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "S", Kind: "text", Body: "x", SentAt: now}))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "g2m", GroupJID: "g2@g.us", SenderJID: "x", SenderName: "S", Kind: "text", Body: "y", SentAt: now}))

	stub := &stubSummarizer{
		errByGroup: map[string]error{"Bad": errors.New("nope")},
	}
	sections, ids, err := BuildSummary(ctx, s, stub, nil)
	require.NoError(t, err, "one bad group must not abort the run")
	require.Len(t, sections, 1)
	require.Equal(t, "Good", sections[0].GroupName)
	require.Equal(t, []string{"g1m"}, ids, "Bad group's message IDs are not in the mark-summarized list")
}

func TestBuildSummary_NoGroupsReturnsEmpty(t *testing.T) {
	s, ctx := newSummaryTestStore(t)

	stub := &stubSummarizer{}
	sections, ids, err := BuildSummary(ctx, s, stub, nil)
	require.NoError(t, err)
	require.Empty(t, sections)
	require.Empty(t, ids)
	require.Empty(t, stub.calls, "summarizer must not be called when no opted-in groups exist")
}
```

- [ ] **Step 3: Run, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test -tags integration ./tools/whatsapp/ -v`
Expected: all tests pass — store + decode + pairing + summary + integration (~25+ tests total).

- [ ] **Step 4: Commit**

```bash
git add tools/whatsapp/summary.go tools/whatsapp/summary_integration_test.go
git commit -m "feat(whatsapp): BuildSummary orchestrator over store + summarizer"
```

---

## Task 5 — Wire into `cmd/darek/daily_digest.go`

**Files:**
- Modify: `cmd/darek/daily_digest.go`

- [ ] **Step 1: Add imports**

In `cmd/darek/daily_digest.go`, add to the existing import block:

```go
"darek/db"
"darek/llm"
"darek/tools/whatsapp"
```

(Slot in alphabetically with the existing `darek/...` imports.)

- [ ] **Step 2: Open Postgres pool when WhatsApp is enabled**

After `cfg, err := config.Load(cfgPath)` (around the top of `runDailyDigest`), and after the existing `cfg.CalendarDigest` validation, add:

```go
	var pool *db.Pool
	if cfg.WhatsApp.Enabled {
		dsn, err := config.ResolveSecret("env:" + cfg.Postgres.URLEnv)
		if err != nil {
			return fmt.Errorf("postgres dsn (whatsapp digest): %w", err)
		}
		pool, err = db.Open(ctx, dsn)
		if err != nil {
			return fmt.Errorf("db (whatsapp digest): %w", err)
		}
		defer pool.Close()
	}
```

(This runs after the existing `srcs := calendar.NewSources()` block? — No: place it AFTER `cfg` is loaded and validated, BEFORE the calendar-source build. The exact position doesn't matter functionally; pick a spot that reads logically. Goal: `pool` is in scope by the time the WhatsApp block needs it.)

- [ ] **Step 3: Build the WhatsApp summary section**

After `html := digest.RenderHTML(buckets, now)` and before the `subject := d.Subject` block, add:

```go
	var waText, waHTML string
	var summarizedIDs []string
	if cfg.WhatsApp.Enabled {
		apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
		switch {
		case err != nil || apiKey == "":
			fmt.Fprintf(os.Stderr, "info: openai not configured, skipping whatsapp digest section\n")
		default:
			llmClient, err := llm.New(llm.Options{
				APIKey:  apiKey,
				BaseURL: cfg.OpenAI.BaseURL,
				Model:   cfg.OpenAI.Model,
				Timeout: cfg.Agent.LLMTimeout,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: llm client for whatsapp digest: %v\n", err)
			} else {
				waStore := whatsapp.NewStore(pool)
				summarizer := whatsapp.NewSummarizer(llmClient)
				logf := func(format string, a ...any) {
					fmt.Fprintf(os.Stderr, "warn: "+format, a...)
				}
				sections, ids, err := whatsapp.BuildSummary(ctx, waStore, summarizer, logf)
				switch {
				case err != nil:
					fmt.Fprintf(os.Stderr, "warn: whatsapp summary failed: %v\n", err)
				case len(sections) > 0:
					waText = whatsapp.RenderText(sections)
					waHTML = whatsapp.RenderHTML(sections)
					summarizedIDs = ids
				}
			}
		}
	}

	if waText != "" {
		text += "\n\n" + waText
	}
	if waHTML != "" {
		html += waHTML
	}
```

(Note: `text` and `html` here are the calendar-section variables from the lines just above. If they're currently named differently in the file — read first — adapt.)

- [ ] **Step 4: Mark summarized after a successful send**

After the existing `if err := sender.Send(...); err != nil { return ...; }` block, add:

```go
	if len(summarizedIDs) > 0 {
		waStore := whatsapp.NewStore(pool)
		if err := waStore.MarkSummarized(ctx, summarizedIDs); err != nil {
			fmt.Fprintf(os.Stderr, "warn: mark whatsapp summarized: %v\n", err)
		}
	}
```

- [ ] **Step 5: Build**

Run: `cd /Users/bklimczak/Projects/darek && go build ./...`
Expected: clean.

- [ ] **Step 6: Run all tests**

Run: `cd /Users/bklimczak/Projects/darek && make test`
Expected: PASS.

- [ ] **Step 7: Lint**

Run: `cd /Users/bklimczak/Projects/darek && make lint`
Expected: no warnings.

- [ ] **Step 8: Commit**

```bash
git add cmd/darek/daily_digest.go
git commit -m "feat(daily-digest): append WhatsApp summary section + mark on send-success"
```

---

## Task 6 — README + final verification

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Update the WhatsApp + daily-digest sections**

In `README.md`, find the existing "WhatsApp" section and append (after the "What's stored" paragraph, before the "Sub-projects B/C" line):

```markdown

### Daily digest summary

When the daily-digest cron runs (`darek calendar daily-digest`) and at least one WhatsApp group is opted-in, the email gains a "WhatsApp" section after the calendar one. Each section lists one short LLM-generated summary per group, covering messages received since the previous digest. Only groups with new messages appear; if no group has activity since the last run, the WhatsApp section is omitted.

The lookback is capped at 7 days — if darek serve was offline longer than that, older messages are not summarized.
```

Find the existing "Daily digest email" subsection under "Calendars" (around line 156) and add a one-line cross-reference at the end of that block:

```markdown

When WhatsApp is configured (see WhatsApp section below), this email also includes a per-group summary of recent WhatsApp activity.
```

- [ ] **Step 2: Final test + lint**

Run: `cd /Users/bklimczak/Projects/darek && make test && make lint`
Expected: clean.

- [ ] **Step 3: Smoke build the binary**

Run: `cd /Users/bklimczak/Projects/darek && CGO_ENABLED=0 go build -o /tmp/darek-test ./cmd/darek`
Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs(readme): document the WhatsApp section in the daily digest email"
```

- [ ] **Step 5: Manual smoke (optional, post-deploy)**

After bumping the image tag and rolling the helm release:

```bash
# trigger the cron manually
kubectl -n darek create job --from=cronjob/darek-daily-digest digest-test

# tail logs
kubectl -n darek logs -f job/digest-test

# verify the email arrived with both calendar AND whatsapp sections.
# trigger again immediately — second run's whatsapp section should be empty
# (or absent) because all messages from the first run were marked summarized.
```
