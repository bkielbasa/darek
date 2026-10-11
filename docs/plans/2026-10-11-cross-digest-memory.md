# Cross-Digest Memory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract durable, structured memories (agreements, events, facts about people) from recent WhatsApp conversation during the daily digest, persist them in a `memories` table, and surface the still-relevant ones as "Follow-ups" / "People" sections in the email — without ever changing the calendar automatically.

**Architecture:** New `memories` table plus a `memories_extracted_at` cursor on `whatsapp_messages` (independent of `summarized_at`). New `memory.Extractor` (LLM via a `Chat` interface, JSON output) and `memory.BuildMemories` orchestrator (fetch unextracted messages per opted-in group → extract → upsert by `dedupe_key` → mark extracted). New `memory.Recall` + renderers produce the digest sections. `cmd/darek/daily_digest.go` composes calendar → follow-ups/people → whatsapp. A `memory.list` agent tool exposes recall in chat.

**Tech Stack:** Existing Go stack (pgx v5, openai-go, `html/template`/`strings`). No new direct deps. Tests use `github.com/stretchr/testify/require` and the project's pg testcontainer helper.

**Design source:** [docs/specs/2026-10-11-cross-digest-memory-design.md](../specs/2026-10-11-cross-digest-memory-design.md) (draft 2026-10-11).

**Out of scope (deferred):** Automatic calendar changes; mail/calendar as extraction sources; embedding/pgvector recall; near-duplicate consolidation; chat-side status editing; per-group extraction toggles; late-reply detection; recurring dates.

---

## File Map

| Path | Responsibility |
|---|---|
| `db/migrations/0015_memory.up.sql` | (create) `memories` table + `whatsapp_messages.memories_extracted_at` + indexes. |
| `memory/memories.go` | (create) `Candidate`, `Memory`, `slug`/`dedupeKey`, and the `memories` store methods. |
| `memory/memories_integration_test.go` | (create) integration tests for the store methods. |
| `tools/whatsapp/store.go` | (modify) add `UnextractedMessages`, `MarkMemoriesExtracted`. |
| `tools/whatsapp/store_test.go` | (modify) integration tests for the two new methods. |
| `memory/extract.go` | (create) `Chat` interface, `Extractor`, JSON prompting/parsing. |
| `memory/extract_test.go` | (create) unit tests with a fake `Chat`. |
| `memory/orchestrate.go` | (create) `TranscriptSource`, `ExtractorInterface`, `Stats`, `BuildMemories`. |
| `memory/orchestrate_integration_test.go` | (create) integration tests for `BuildMemories`. |
| `memory/render.go` | (create) `FollowupView`, `Recall`, `RenderText`, `RenderHTML`. |
| `memory/render_test.go` | (create) unit tests for recall + renderers. |
| `memory/recall_tool.go` | (create) `memory.list` agent tool. |
| `memory/recall_tool_test.go` | (create) tool unit tests. |
| `config/types.go` | (modify) add `Memory.Enabled`. |
| `obs/metrics.go` | (modify) add `MemoriesExtracted`, `MemoriesSaved` counters. |
| `obs/metrics_test.go` | (modify) assert the new counters initialize. |
| `cmd/darek/daily_digest.go` | (modify) run `BuildMemories` + recall, append sections. |
| `cmd/darek/chat.go` | (modify) register `memory.ListTool`. |
| `README.md` | (modify) document cross-digest memory. |

---

## Task 1 — Migration + memory store (TDD, integration)

**Files:**
- Create: `db/migrations/0015_memory.up.sql`
- Create: `memory/memories.go`
- Create: `memory/memories_integration_test.go`

- [ ] **Step 1: Create the migration**

`db/migrations/0015_memory.up.sql`:

```sql
CREATE TABLE memories (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    kind         text NOT NULL,
    summary      text NOT NULL,
    details      text NOT NULL DEFAULT '',
    people       text[] NOT NULL DEFAULT '{}',
    groups       text[] NOT NULL DEFAULT '{}',
    due_at       timestamptz,
    status       text NOT NULL DEFAULT 'active',
    dedupe_key   text NOT NULL,
    source       text NOT NULL DEFAULT 'digest',
    source_refs  text[] NOT NULL DEFAULT '{}',
    search       tsvector GENERATED ALWAYS AS (
        to_tsvector('simple'::regconfig,
            summary || ' ' || details || ' ' ||
            immutable_array_to_string(people, ' ') || ' ' ||
            immutable_array_to_string(groups, ' '))
    ) STORED
);

CREATE UNIQUE INDEX memories_dedupe_key_uniq ON memories (dedupe_key);
CREATE INDEX memories_search_gin  ON memories USING gin(search);
CREATE INDEX memories_people_gin  ON memories USING gin(people);
CREATE INDEX memories_active_due  ON memories (due_at) WHERE status = 'active';
CREATE INDEX memories_kind_status ON memories (kind, status, last_seen_at DESC);

ALTER TABLE whatsapp_messages
    ADD COLUMN memories_extracted_at timestamptz;

CREATE INDEX idx_whatsapp_messages_unextracted
    ON whatsapp_messages (group_jid, sent_at)
    WHERE memories_extracted_at IS NULL;
```

(`immutable_array_to_string` is created in `0001_initial.up.sql`. No `down` migration; matches the project convention.)

- [ ] **Step 2: Write failing tests**

Create `memory/memories_integration_test.go`:

```go
//go:build integration

package memory

import (
	"context"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func newMemStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	return NewStore(db.Wrap(pool)), context.Background()
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestSaveMemories_InsertAndDedupe(t *testing.T) {
	s, ctx := newMemStore(t)
	due := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)

	n, err := s.SaveMemories(ctx, []Candidate{{
		Kind: "agreement", Summary: "Dinner with Marek on Sunday",
		People: []string{"Marek"}, DueAt: ptrTime(due), DedupeKey: "Dinner Marek Sunday",
	}}, "Family", []string{"m1"})
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// Same key (different casing) → update, not duplicate.
	n, err = s.SaveMemories(ctx, []Candidate{{
		Kind: "agreement", Summary: "Dinner with Marek this Sunday",
		People: []string{"Marek"}, DedupeKey: "dinner-marek-sunday",
	}}, "Friends", []string{"m2"})
	require.NoError(t, err)
	require.Equal(t, 1, n)

	items, err := s.ActiveItems(ctx, 14, 7, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "Dinner with Marek this Sunday", items[0].Summary)
	require.ElementsMatch(t, []string{"Family", "Friends"}, items[0].Groups)
	require.ElementsMatch(t, []string{"m1", "m2"}, items[0].SourceRefs)
	require.NotNil(t, items[0].DueAt)
}

func TestSaveMemories_PreservesStatusAndDueAt(t *testing.T) {
	s, ctx := newMemStore(t)
	due := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)

	n, err := s.SaveMemories(ctx, []Candidate{{
		Kind: "agreement", Summary: "Send the plan", DedupeKey: "send-plan", DueAt: ptrTime(due),
	}}, "Work", []string{"w1"})
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := s.ListMemories(ctx, "Send the plan", "", "", 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NoError(t, s.SetMemoryStatus(ctx, got[0].ID, "done"))

	// Re-emit with a null due date: status must stay "done", due_at must survive.
	n, err = s.SaveMemories(ctx, []Candidate{{
		Kind: "agreement", Summary: "Send the plan", DedupeKey: "send-plan",
	}}, "Work", []string{"w2"})
	require.NoError(t, err)
	require.Equal(t, 1, n)

	items, err := s.ActiveItems(ctx, 14, 7, 10)
	require.NoError(t, err)
	require.Empty(t, items, "done memory must not appear as active")

	all, err := s.ListMemories(ctx, "", "agreement", "", 10)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "done", all[0].Status)
	require.NotNil(t, all[0].DueAt)
}

func TestActiveItems_FiltersAndOrders(t *testing.T) {
	s, ctx := newMemStore(t)
	now := time.Now().UTC()

	_, err := s.SaveMemories(ctx, []Candidate{
		{Kind: "agreement", Summary: "Overdue thing", DedupeKey: "overdue", DueAt: ptrTime(now.Add(-48 * time.Hour))},
		{Kind: "event", Summary: "Soon thing", DedupeKey: "soon", DueAt: ptrTime(now.Add(48 * time.Hour))},
		{Kind: "event", Summary: "Far thing", DedupeKey: "far", DueAt: ptrTime(now.Add(60 * 24 * time.Hour))},
		{Kind: "agreement", Summary: "Undated recent", DedupeKey: "undated-recent"},
		{Kind: "person_fact", Summary: "Anna likes tea", DedupeKey: "anna-tea"},
	}, "G", []string{"x"})
	require.NoError(t, err)

	items, err := s.ActiveItems(ctx, 14, 7, 10)
	require.NoError(t, err)
	keys := []string{}
	for _, m := range items {
		keys = append(keys, m.DedupeKey)
	}
	require.Equal(t, []string{"overdue", "soon", "undated-recent"}, keys, "ordered by due_at ASC NULLS LAST; far + person_fact excluded")
}

func TestPersonFacts_Window(t *testing.T) {
	s, ctx := newMemStore(t)
	_, err := s.SaveMemories(ctx, []Candidate{{
		Kind: "person_fact", Summary: "Kuba plays football", People: []string{"Anna"}, DedupeKey: "kuba-football",
	}}, "Family", []string{"p1"})
	require.NoError(t, err)

	people, err := s.PersonFacts(ctx, 3, 8)
	require.NoError(t, err)
	require.Len(t, people, 1)
	require.Equal(t, "kuba-football", people[0].DedupeKey)

	// A stale person fact (older than the window) is excluded.
	_, err = s.pool.Exec(ctx, `UPDATE memories SET last_seen_at = now() - interval '10 days' WHERE dedupe_key = 'kuba-football'`)
	require.NoError(t, err)
	people, err = s.PersonFacts(ctx, 3, 8)
	require.NoError(t, err)
	require.Empty(t, people)
}

func TestListMemories_Filters(t *testing.T) {
	s, ctx := newMemStore(t)
	_, err := s.SaveMemories(ctx, []Candidate{
		{Kind: "agreement", Summary: "Berlin trip planning", DedupeKey: "berlin"},
		{Kind: "person_fact", Summary: "Marek fears heights", DedupeKey: "marek-heights"},
	}, "G", []string{"x"})
	require.NoError(t, err)

	byQuery, err := s.ListMemories(ctx, "Berlin", "", "", 5)
	require.NoError(t, err)
	require.Len(t, byQuery, 1)
	require.Equal(t, "berlin", byQuery[0].DedupeKey)

	byKind, err := s.ListMemories(ctx, "", "person_fact", "", 5)
	require.NoError(t, err)
	require.Len(t, byKind, 1)
	require.Equal(t, "marek-heights", byKind[0].DedupeKey)
}
```

- [ ] **Step 3: Run, expect failure**

Run: `go test -tags integration ./memory/ -run "TestSaveMemories|TestActiveItems|TestPersonFacts|TestListMemories" -v`
Expected: FAIL — `Candidate`, `SaveMemories`, etc. undefined.

- [ ] **Step 4: Implement `memory/memories.go`**

```go
package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Candidate is one memory proposed by the extractor. DedupeKey is the stable
// identity of the memory; an empty key is derived from kind + summary.
type Candidate struct {
	Kind      string
	Summary   string
	Details   string
	People    []string
	DueAt     *time.Time
	DedupeKey string
}

// Memory is a persisted structured memory row.
type Memory struct {
	ID         uuid.UUID
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastSeenAt time.Time
	Kind       string
	Summary    string
	Details    string
	People     []string
	Groups     []string
	DueAt      *time.Time
	Status     string
	DedupeKey  string
	Source     string
	SourceRefs []string
}

const memoryColumns = `id, created_at, updated_at, last_seen_at, kind, summary, details,
	people, groups, due_at, status, dedupe_key, source, source_refs`

// slug lowercases s and collapses every run of non-alphanumeric characters to a
// single "-". ASCII-only: non-ASCII letters are separators, so name variants can
// collide — acceptable for a personal system (documented in the spec risks).
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// dedupeKey normalizes the candidate's key, deriving one when absent.
func dedupeKey(c Candidate) string {
	if k := slug(c.DedupeKey); k != "" {
		return k
	}
	return slug(c.Kind + "-" + c.Summary)
}

func scanMemory(row pgx.Row) (Memory, error) {
	var m Memory
	err := row.Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt, &m.LastSeenAt, &m.Kind,
		&m.Summary, &m.Details, &m.People, &m.Groups, &m.DueAt, &m.Status,
		&m.DedupeKey, &m.Source, &m.SourceRefs)
	return m, err
}

func collectMemories(rows pgx.Rows) ([]Memory, error) {
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt, &m.LastSeenAt,
			&m.Kind, &m.Summary, &m.Details, &m.People, &m.Groups, &m.DueAt,
			&m.Status, &m.DedupeKey, &m.Source, &m.SourceRefs); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveMemories upserts candidates by dedupe_key. Returns the number of rows
// written. On conflict it refreshes summary/details/last_seen_at, unions
// groups + source_refs, COALESCEs due_at, and preserves status.
func (s *Store) SaveMemories(ctx context.Context, cands []Candidate, group string, sourceRefs []string) (int, error) {
	if len(cands) == 0 {
		return 0, nil
	}
	if sourceRefs == nil {
		sourceRefs = []string{}
	}
	groups := []string{}
	if group != "" {
		groups = []string{group}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	saved := 0
	for _, c := range cands {
		people := c.People
		if people == nil {
			people = []string{}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO memories
			    (kind, summary, details, people, groups, due_at, dedupe_key, source, source_refs)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'digest',$8)
			ON CONFLICT (dedupe_key) DO UPDATE SET
			    updated_at   = now(),
			    last_seen_at = now(),
			    summary      = EXCLUDED.summary,
			    details      = EXCLUDED.details,
			    people       = EXCLUDED.people,
			    groups       = (SELECT array(SELECT DISTINCT unnest(memories.groups || EXCLUDED.groups))),
			    source_refs  = (SELECT array(SELECT DISTINCT unnest(memories.source_refs || EXCLUDED.source_refs))),
			    due_at       = COALESCE(EXCLUDED.due_at, memories.due_at)
		`, c.Kind, c.Summary, c.Details, people, groups, c.DueAt, dedupeKey(c), sourceRefs)
		if err != nil {
			return saved, fmt.Errorf("upsert memory %q: %w", dedupeKey(c), err)
		}
		saved++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	if s.m != nil {
		s.m.MemoriesSaved.Add(ctx, int64(saved))
	}
	return saved, nil
}

// ActiveItems returns active agreements/events due before now+horizonDays, or
// undated but seen within recentDays. Ordered due_at ASC NULLS LAST, then recency.
func (s *Store) ActiveItems(ctx context.Context, horizonDays, recentDays, limit int) ([]Memory, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+memoryColumns+`
		  FROM memories
		 WHERE status = 'active'
		   AND kind IN ('agreement', 'event')
		   AND ((due_at IS NOT NULL AND due_at < now() + make_interval(days => $1))
		        OR (due_at IS NULL AND last_seen_at >= now() - make_interval(days => $2)))
		 ORDER BY due_at ASC NULLS LAST, last_seen_at DESC
		 LIMIT $3
	`, horizonDays, recentDays, limit)
	if err != nil {
		return nil, fmt.Errorf("query active: %w", err)
	}
	return collectMemories(rows)
}

// PersonFacts returns active person_fact rows seen within the last sinceDays.
func (s *Store) PersonFacts(ctx context.Context, sinceDays, limit int) ([]Memory, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+memoryColumns+`
		  FROM memories
		 WHERE status = 'active'
		   AND kind = 'person_fact'
		   AND last_seen_at >= now() - make_interval(days => $1)
		 ORDER BY last_seen_at DESC
		 LIMIT $2
	`, sinceDays, limit)
	if err != nil {
		return nil, fmt.Errorf("query people: %w", err)
	}
	return collectMemories(rows)
}

// ListMemories is the chat-recall query. Empty query/kind/status are wildcards.
func (s *Store) ListMemories(ctx context.Context, query, kind, status string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 8
	}
	var (
		conds []string
		args  []any
	)
	if query != "" {
		args = append(args, query)
		conds = append(conds, fmt.Sprintf("search @@ plainto_tsquery('simple', $%d)", len(args)))
	}
	if kind != "" {
		args = append(args, kind)
		conds = append(conds, fmt.Sprintf("kind = $%d", len(args)))
	}
	if status != "" {
		args = append(args, status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM memories
		%s
		ORDER BY last_seen_at DESC
		LIMIT $%d
	`, memoryColumns, where, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("query list: %w", err)
	}
	return collectMemories(rows)
}

// SetMemoryStatus updates one memory's status. No caller exposes this in v1.
func (s *Store) SetMemoryStatus(ctx context.Context, id uuid.UUID, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE memories SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	return err
}
```

- [ ] **Step 5: Run, expect pass**

Run: `go test -tags integration ./memory/ -v`
Expected: all tests pass (existing notes tests + 5 new memory tests).

- [ ] **Step 6: Commit**

```bash
git add db/migrations/0015_memory.up.sql memory/memories.go memory/memories_integration_test.go
git commit -m "feat(memory): structured memories table + store"
```

---

## Task 2 — WhatsApp memory cursor (TDD, integration)

**Files:**
- Modify: `tools/whatsapp/store.go`
- Modify: `tools/whatsapp/store_test.go`

- [ ] **Step 1: Append failing tests**

Append to `tools/whatsapp/store_test.go`:

```go
func TestStore_UnextractedMessages(t *testing.T) {
	s, ctx := newTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "G1"))
	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Second)
	rec := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "old", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "x", Kind: "text", Body: "long ago", SentAt: old}))
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "rec", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "x", Kind: "text", Body: "recent", SentAt: rec}))

	msgs, err := s.UnextractedMessages(ctx, "g1@g.us", 7)
	require.NoError(t, err)
	require.Len(t, msgs, 1, "old message outside 7-day window excluded")
	require.Equal(t, "rec", msgs[0].ID)

	require.NoError(t, s.MarkMemoriesExtracted(ctx, []string{"rec"}))
	msgs, err = s.UnextractedMessages(ctx, "g1@g.us", 7)
	require.NoError(t, err)
	require.Empty(t, msgs)
}

func TestStore_MarkMemoriesExtracted_DoesNotTouchSummarized(t *testing.T) {
	s, ctx := newTestStore(t)

	require.NoError(t, s.UpsertGroup(ctx, "g1@g.us", "G1"))
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.InsertMessage(ctx, Message{ID: "m1", GroupJID: "g1@g.us", SenderJID: "x", SenderName: "x", Kind: "text", Body: "a", SentAt: now}))

	require.NoError(t, s.MarkMemoriesExtracted(ctx, []string{"m1", "missing"})) // idempotent, ignores missing

	// The summary cursor is untouched: m1 is still unsummarized.
	unsum, err := s.UnsummarizedMessages(ctx, "g1@g.us", 7)
	require.NoError(t, err)
	require.Len(t, unsum, 1)
}

func TestStore_MarkMemoriesExtracted_EmptyIsNoop(t *testing.T) {
	s, _ := newTestStore(t)
	require.NoError(t, s.MarkMemoriesExtracted(nil))
}
```

- [ ] **Step 2: Run, expect failure**

Run: `go test -tags integration ./tools/whatsapp/ -run "TestStore_UnextractedMessages|TestStore_MarkMemoriesExtracted" -v`
Expected: FAIL — methods undefined.

- [ ] **Step 3: Implement**

Append to `tools/whatsapp/store.go`:

```go
// UnextractedMessages returns messages where memories_extracted_at IS NULL and
// sent_at >= now() - lookbackDays. Ascending by sent_at.
func (s *Store) UnextractedMessages(ctx context.Context, groupJID string, lookbackDays int) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, group_jid, sender_jid, sender_name, kind, body, sent_at
		  FROM whatsapp_messages
		 WHERE group_jid = $1
		   AND memories_extracted_at IS NULL
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

// MarkMemoriesExtracted sets memories_extracted_at = now() for the given IDs.
// Idempotent; does not touch summarized_at.
func (s *Store) MarkMemoriesExtracted(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE whatsapp_messages SET memories_extracted_at = now() WHERE id = ANY($1)`, ids)
	return err
}
```

- [ ] **Step 4: Run, expect pass**

Run: `go test -tags integration ./tools/whatsapp/ -v`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add tools/whatsapp/store.go tools/whatsapp/store_test.go
git commit -m "feat(whatsapp): memories_extracted cursor + store methods"
```

---

## Task 3 — Extractor (TDD, unit)

**Files:**
- Create: `memory/extract.go`
- Create: `memory/extract_test.go`

- [ ] **Step 1: Implement `memory/extract.go`**

```go
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
```

Import note: `openai.ResponseFormatJSONObjectParam` is an alias of `shared.ResponseFormatJSONObjectParam` in openai-go v1.12.0 (verified). The fallback also works without `ResponseFormat` because `parseCandidates` strips code fences.

- [ ] **Step 2: Write tests**

Create `memory/extract_test.go`:

```go
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
```

- [ ] **Step 3: Run**

Run: `go test ./memory/ -run "TestExtract|TestParse|TestSlug" -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add memory/extract.go memory/extract_test.go
git commit -m "feat(memory): LLM extractor for structured memories"
```

---

## Task 4 — `BuildMemories` orchestrator (TDD, integration)

**Files:**
- Create: `memory/orchestrate.go`
- Create: `memory/orchestrate_integration_test.go`

- [ ] **Step 1: Implement `memory/orchestrate.go`**

```go
package memory

import (
	"context"
	"fmt"

	"darek/tools/whatsapp"
)

// memoriesLookbackDays bounds how far back extraction reaches. Bounded so a
// long darek-serve outage doesn't dump weeks of messages into one run.
const memoriesLookbackDays = 7

// TranscriptSource is the subset of *whatsapp.Store used by BuildMemories.
type TranscriptSource interface {
	OptedInGroups(ctx context.Context) ([]whatsapp.Group, error)
	UnextractedMessages(ctx context.Context, groupJID string, lookbackDays int) ([]whatsapp.Message, error)
	MarkMemoriesExtracted(ctx context.Context, ids []string) error
}

// ExtractorInterface is the subset of *Extractor used by BuildMemories.
type ExtractorInterface interface {
	Extract(ctx context.Context, groupName string, msgs []whatsapp.Message) ([]Candidate, error)
}

// Stats summarizes one BuildMemories run.
type Stats struct {
	Groups       int
	Extracted    int
	Saved        int
	FailedGroups int
}

// BuildMemories extracts and persists memories for every opted-in group with
// unextracted messages. Per-group failures are logged and skipped; only an
// OptedInGroups failure aborts the run.
func BuildMemories(
	ctx context.Context,
	src TranscriptSource,
	store *Store,
	extractor ExtractorInterface,
	logger func(format string, a ...any),
) (Stats, error) {
	if logger == nil {
		logger = func(string, ...any) {}
	}
	var st Stats

	groups, err := src.OptedInGroups(ctx)
	if err != nil {
		return st, fmt.Errorf("opted-in groups: %w", err)
	}
	st.Groups = len(groups)

	for _, g := range groups {
		msgs, err := src.UnextractedMessages(ctx, g.JID, memoriesLookbackDays)
		if err != nil {
			logger("memory: unextracted for %q: %v\n", g.Name, err)
			st.FailedGroups++
			continue
		}
		if len(msgs) == 0 {
			continue
		}

		cands, err := extractor.Extract(ctx, g.Name, msgs)
		if err != nil {
			logger("memory: extract for %q: %v\n", g.Name, err)
			st.FailedGroups++
			continue
		}

		ids := messageIDs(msgs)
		if len(cands) > 0 {
			n, err := store.SaveMemories(ctx, cands, g.Name, ids)
			if err != nil {
				logger("memory: save for %q: %v\n", g.Name, err)
				st.FailedGroups++
				continue
			}
			st.Extracted += len(cands)
			st.Saved += n
		}

		if err := src.MarkMemoriesExtracted(ctx, ids); err != nil {
			logger("memory: mark extracted for %q: %v\n", g.Name, err)
		}
	}
	return st, nil
}

func messageIDs(msgs []whatsapp.Message) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}
```

- [ ] **Step 2: Write tests**

Create `memory/orchestrate_integration_test.go`:

```go
//go:build integration

package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"
	"darek/tools/whatsapp"

	"github.com/stretchr/testify/require"
)

// stubSource implements TranscriptSource with in-memory data.
type stubSource struct {
	groups    []whatsapp.Group
	msgs      map[string][]whatsapp.Message
	marked    []string
	markCalls int
}

func (s *stubSource) OptedInGroups(_ context.Context) ([]whatsapp.Group, error) {
	return s.groups, nil
}
func (s *stubSource) UnextractedMessages(_ context.Context, jid string, _ int) ([]whatsapp.Message, error) {
	return s.msgs[jid], nil
}
func (s *stubSource) MarkMemoriesExtracted(_ context.Context, ids []string) error {
	s.markCalls++
	s.marked = append(s.marked, ids...)
	return nil
}

type stubExtractor struct {
	byGroup map[string][]Candidate
	errByGroup map[string]error
	calls   []string
}

func (s *stubExtractor) Extract(_ context.Context, group string, _ []whatsapp.Message) ([]Candidate, error) {
	s.calls = append(s.calls, group)
	if err, ok := s.errByGroup[group]; ok {
		return nil, err
	}
	return s.byGroup[group], nil
}

func newOrchStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))
	return NewStore(db.Wrap(pool)), context.Background()
}

func waMsg(id, group string) whatsapp.Message {
	return whatsapp.Message{ID: id, GroupJID: group, SenderJID: "x", SenderName: "S", Kind: "text", Body: "b", SentAt: time.Now()}
}

func TestBuildMemories_HappyPath(t *testing.T) {
	store, ctx := newOrchStore(t)
	src := &stubSource{
		groups: []whatsapp.Group{{JID: "g1@g.us", Name: "Family"}, {JID: "g2@g.us", Name: "Quiet"}},
		msgs: map[string][]whatsapp.Message{
			"g1@g.us": {waMsg("m1", "g1@g.us"), waMsg("m2", "g1@g.us")},
		},
	}
	ex := &stubExtractor{byGroup: map[string][]Candidate{
		"Family": {{Kind: "agreement", Summary: "Dinner Sunday", DedupeKey: "dinner-sunday"}},
	}}

	st, err := BuildMemories(ctx, src, store, ex, nil)
	require.NoError(t, err)
	require.Equal(t, 2, st.Groups)
	require.Equal(t, 1, st.Extracted)
	require.Equal(t, 1, st.Saved)
	require.Equal(t, 0, st.FailedGroups)
	require.ElementsMatch(t, []string{"m1", "m2"}, src.marked)

	items, err := store.ActiveItems(ctx, 14, 7, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "Family", items[0].Groups[0])
}

func TestBuildMemories_NoCandidatesStillMarks(t *testing.T) {
	store, ctx := newOrchStore(t)
	src := &stubSource{
		groups: []whatsapp.Group{{JID: "g1@g.us", Name: "Chatty"}},
		msgs:   map[string][]whatsapp.Message{"g1@g.us": {waMsg("m1", "g1@g.us")}},
	}
	ex := &stubExtractor{byGroup: map[string][]Candidate{"Chatty": nil}}

	st, err := BuildMemories(ctx, src, store, ex, nil)
	require.NoError(t, err)
	require.Equal(t, 0, st.Saved)
	require.Equal(t, []string{"m1"}, src.marked, "read-but-nothing-durable messages are still marked")
}

func TestBuildMemories_FailedGroupDoesNotAbort(t *testing.T) {
	store, ctx := newOrchStore(t)
	src := &stubSource{
		groups: []whatsapp.Group{{JID: "g1@g.us", Name: "Good"}, {JID: "g2@g.us", Name: "Bad"}},
		msgs: map[string][]whatsapp.Message{
			"g1@g.us": {waMsg("g1m", "g1@g.us")},
			"g2@g.us": {waMsg("g2m", "g2@g.us")},
		},
	}
	ex := &stubExtractor{
		byGroup:    map[string][]Candidate{"Good": {{Kind: "event", Summary: "x", DedupeKey: "x"}}},
		errByGroup: map[string]error{"Bad": errors.New("nope")},
	}

	st, err := BuildMemories(ctx, src, store, ex, nil)
	require.NoError(t, err)
	require.Equal(t, 1, st.FailedGroups)
	require.Equal(t, 1, st.Saved)
	require.Equal(t, []string{"g1m"}, src.marked, "failed group's IDs are not marked (retry)")
}
```

- [ ] **Step 3: Run**

Run: `go test -tags integration ./memory/ -run TestBuildMemories -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add memory/orchestrate.go memory/orchestrate_integration_test.go
git commit -m "feat(memory): BuildMemories orchestrator"
```

---

## Task 5 — Recall + rendering (TDD, unit)

**Files:**
- Create: `memory/render.go`
- Create: `memory/render_test.go`

- [ ] **Step 1: Implement `memory/render.go`**

```go
package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	followupHorizonDays    = 14
	followupRecentDays     = 7
	followupLimit          = 10
	personFactLookbackDays = 3
	personFactLimit        = 8
)

// FollowupView is what the digest renders.
type FollowupView struct {
	Items  []Memory
	People []Memory
}

// Recall fetches active follow-ups and recent person facts. Partial results are
// returned alongside a joined error.
func Recall(ctx context.Context, store *Store) (FollowupView, error) {
	items, ierr := store.ActiveItems(ctx, followupHorizonDays, followupRecentDays, followupLimit)
	people, perr := store.PersonFacts(ctx, personFactLookbackDays, personFactLimit)
	return FollowupView{Items: items, People: people}, errors.Join(ierr, perr)
}

// RenderText renders the follow-ups + people sections. Empty view → "".
func RenderText(v FollowupView) string {
	if len(v.Items) == 0 && len(v.People) == 0 {
		return ""
	}
	var b strings.Builder
	if len(v.Items) > 0 {
		b.WriteString("Follow-ups\n")
		b.WriteString(strings.Repeat("─", 10))
		b.WriteString("\n\n")
		for _, m := range v.Items {
			fmt.Fprintf(&b, "▸ %s\n", followupLine(m))
		}
	}
	if len(v.People) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("People\n")
		b.WriteString(strings.Repeat("─", 6))
		b.WriteString("\n\n")
		for _, m := range v.People {
			fmt.Fprintf(&b, "• %s\n", personLine(m))
		}
	}
	return b.String()
}

func followupLine(m Memory) string {
	when := "recent"
	if m.DueAt != nil {
		d := m.DueAt.In(time.Local)
		if d.Before(time.Now()) {
			when = "overdue since " + d.Format("Mon 02 Jan")
		} else {
			when = d.Format("Mon 02 Jan 15:04")
		}
	}
	line := fmt.Sprintf("%s — %s", when, m.Summary)
	if len(m.Groups) > 0 && m.Groups[0] != "" {
		line += " (" + m.Groups[0] + ")"
	}
	return line
}

func personLine(m Memory) string {
	if len(m.People) > 0 {
		return m.People[0] + " — " + m.Summary
	}
	return m.Summary
}

// RenderHTML renders the sections as inline-styled email-safe HTML. Empty view → "".
func RenderHTML(v FollowupView) string {
	if len(v.Items) == 0 && len(v.People) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<section class="mem-digest" style="margin-top:1.5rem;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;color:#1c1c1c;">`)
	if len(v.Items) > 0 {
		b.WriteString(`<h2 style="margin:0 0 .75rem;font-size:1.05rem;font-weight:600;">Follow-ups</h2>`)
		for _, m := range v.Items {
			b.WriteString(`<div style="background:#fff;border:1px solid #e8e3d8;border-radius:6px;padding:.6rem 1rem;margin-bottom:.4rem;">`)
			fmt.Fprintf(&b, `<div style="line-height:1.45;">%s</div>`, htmlEscape(followupLine(m)))
			b.WriteString(`</div>`)
		}
	}
	if len(v.People) > 0 {
		b.WriteString(`<h2 style="margin:1rem 0 .75rem;font-size:1.05rem;font-weight:600;">People</h2>`)
		for _, m := range v.People {
			fmt.Fprintf(&b, `<div style="line-height:1.5;">• %s</div>`, htmlEscape(personLine(m)))
		}
	}
	b.WriteString(`</section>`)
	return b.String()
}

func htmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
	).Replace(s)
}
```

- [ ] **Step 2: Write tests**

Create `memory/render_test.go`:

```go
package memory

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRenderText_FollowupsAndPeople(t *testing.T) {
	due := time.Date(2026, 10, 18, 19, 0, 0, 0, time.Local)
	v := FollowupView{
		Items: []Memory{
			{Kind: "event", Summary: "Dinner with Marek", DueAt: &due, Groups: []string{"Family"}},
			{Kind: "agreement", Summary: "Send the deploy plan", Groups: []string{"Work"}},
		},
		People: []Memory{
			{Kind: "person_fact", Summary: "son Kuba plays football", People: []string{"Anna"}},
		},
	}
	got := RenderText(v)
	require.Contains(t, got, "Follow-ups")
	require.Contains(t, got, "Dinner with Marek (Family)")
	require.Contains(t, got, "recent — Send the deploy plan (Work)")
	require.Contains(t, got, "People")
	require.Contains(t, got, "Anna — son Kuba plays football")
}

func TestRenderText_Empty(t *testing.T) {
	require.Equal(t, "", RenderText(FollowupView{}))
}

func TestRenderText_Overdue(t *testing.T) {
	past := time.Now().Add(-72 * time.Hour)
	v := FollowupView{Items: []Memory{{Kind: "event", Summary: "Pay the invoice", DueAt: &past}}}
	require.Contains(t, RenderText(v), "overdue since")
}

func TestRenderHTML_EscapesAndEmpty(t *testing.T) {
	require.Equal(t, "", RenderHTML(FollowupView{}))
	v := FollowupView{Items: []Memory{{Kind: "event", Summary: `<script>alert(1)</script>`}}}
	got := RenderHTML(v)
	require.NotContains(t, got, "<script>")
	require.Contains(t, got, "&lt;script&gt;")
}
```

- [ ] **Step 3: Run**

Run: `go test ./memory/ -run TestRender -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add memory/render.go memory/render_test.go
git commit -m "feat(memory): recall + digest renderers"
```

---

## Task 6 — Config flag + metrics

**Files:**
- Modify: `config/types.go`
- Modify: `obs/metrics.go`
- Modify: `obs/metrics_test.go`

- [ ] **Step 1: Add the config flag**

In `config/types.go`, extend the `Memory` struct:

```go
type Memory struct {
	Pgvector       bool   `yaml:"pgvector"`
	EmbeddingModel string `yaml:"embedding_model"`
	Enabled        bool   `yaml:"enabled"` // enables digest memory extraction + recall
}
```

- [ ] **Step 2: Add the counters**

In `obs/metrics.go`, in the `// Memory` block of the `Metrics` struct, add:

```go
	MemoryNotesSaved      metric.Int64Counter
	MemoryNotesRecalled   metric.Int64Counter
	MemoriesExtracted     metric.Int64Counter
	MemoriesSaved         metric.Int64Counter
```

And in the constructor next to `MemoryNotesSaved`:

```go
		MemoriesExtracted:         i64(m.Int64Counter("darek.memory.memories_extracted")),
		MemoriesSaved:             i64(m.Int64Counter("darek.memory.memories_saved")),
```

- [ ] **Step 3: Extend the metrics test**

In `obs/metrics_test.go`, after the existing `MemoryNotesRecalled` assertion, add:

```go
	require.NotNil(t, m.MemoriesExtracted, "MemoriesExtracted not initialized")
	require.NotNil(t, m.MemoriesSaved, "MemoriesSaved not initialized")
```

- [ ] **Step 4: Run**

Run: `go build ./... && go test ./obs/ ./config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add config/types.go obs/metrics.go obs/metrics_test.go
git commit -m "feat(memory): memory.enabled flag + extraction metrics"
```

---

## Task 7 — Wire into `cmd/darek/daily_digest.go`

**Files:**
- Modify: `cmd/darek/daily_digest.go`

- [ ] **Step 1: Add the import**

Add `"darek/memory"` to the `darek/...` import block.

- [ ] **Step 2: Build the memory section**

The WhatsApp block already declares a local `logf`. **Hoist it**: move that `logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, "warn: "+format, a...) }` to just before the WhatsApp block so both blocks share it (remove the inner declaration). Then, after the WhatsApp-summary block (which sets `waText`, `waHTML`, `summarizedIDs`) and before `subject := d.Subject`, add:

```go
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "warn: "+format, a...)
	}

	var memText, memHTML string
	if cfg.Memory.Enabled && cfg.WhatsApp.Enabled && pool != nil {
		apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
		switch {
		case err != nil || apiKey == "":
			fmt.Fprintf(os.Stderr, "info: openai not configured, skipping memory digest section\n")
		default:
			llmClient, err := llm.New(llm.Options{
				APIKey:  apiKey,
				BaseURL: cfg.OpenAI.BaseURL,
				Model:   cfg.OpenAI.Model,
				Timeout: cfg.Agent.LLMTimeout,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: llm client for memory digest: %v\n", err)
			} else {
				waStore := whatsapp.NewStore(pool)
				memStore := memory.NewStore(pool)
				stats, err := memory.BuildMemories(ctx, waStore, memStore, memory.NewExtractor(llmClient), logf)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: memory extraction failed: %v\n", err)
				}
				view, rerr := memory.Recall(ctx, memStore)
				if rerr != nil {
					fmt.Fprintf(os.Stderr, "warn: memory recall failed: %v\n", rerr)
				}
				memText = memory.RenderText(view)
				memHTML = memory.RenderHTML(view)
				fmt.Fprintf(os.Stderr, "info: memory: groups=%d extracted=%d saved=%d failed=%d\n",
					stats.Groups, stats.Extracted, stats.Saved, stats.FailedGroups)
			}
		}
	}
```

(The hoisted `logf` is used by both the WhatsApp and memory blocks; the snippet above shows it for clarity but it is declared once, before the WhatsApp block.)

- [ ] **Step 3: Compose the sections in order**

The current code appends WhatsApp text/HTML to the calendar variables. Change that region so the order is calendar → follow-ups/people → whatsapp:

```go
	text := digest.RenderText(buckets)
	html := digest.RenderHTML(buckets, now)
	if memText != "" {
		text += "\n\n" + memText
		html += memHTML
	}
	if waText != "" {
		text += "\n\n" + waText
		html += waHTML
	}
```

(Read the file: `text`/`html` are currently assigned from `digest.RenderText`/`RenderHTML` and the WhatsApp block appends to them. Move those assignments so the append order above holds.)

- [ ] **Step 4: Build + test + lint**

Run: `go build ./... && make test && make lint`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/darek/daily_digest.go
git commit -m "feat(daily-digest): follow-ups + people sections from memory"
```

---

## Task 8 — `memory.list` chat tool

**Files:**
- Create: `memory/recall_tool.go`
- Create: `memory/recall_tool_test.go`
- Modify: `cmd/darek/chat.go`

- [ ] **Step 1: Implement the tool**

Create `memory/recall_tool.go`:

```go
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ListTool recalls durable structured memories.
type ListTool struct{ Store *Store }

func (ListTool) Name() string { return "memory.list" }
func (ListTool) Description() string {
	return "Recall durable memories (agreements, events, facts about people) learned from earlier digests and conversations."
}
func (ListTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"query":{"type":"string","description":"keywords over summary/details/people; empty for most recent"},
			"kind":{"type":"string","enum":["agreement","event","person_fact","other"]},
			"status":{"type":"string","enum":["active","done","dismissed","superseded"],"default":"active"},
			"limit":{"type":"integer","minimum":1,"maximum":20,"default":8}
		},
		"required":[]
	}`)
}
func (t ListTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query  string `json:"query"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
		Limit  int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	if p.Status == "" {
		p.Status = "active"
	}
	mems, err := t.Store.ListMemories(ctx, p.Query, p.Kind, p.Status, p.Limit)
	if err != nil {
		return "", err
	}
	if len(mems) == 0 {
		return "no matching memories", nil
	}
	var b strings.Builder
	for _, m := range mems {
		when := "—"
		if m.DueAt != nil {
			when = m.DueAt.In(m.DueAt.Location()).Format("2006-01-02")
		}
		fmt.Fprintf(&b, "[%s] %s — %s (%s)\n", when, m.Kind, m.Summary, m.Status)
		if len(m.People) > 0 {
			fmt.Fprintf(&b, "    people: %s\n", strings.Join(m.People, ", "))
		}
	}
	return b.String(), nil
}
```

- [ ] **Step 2: Register in `cmd/darek/chat.go`**

Next to the existing `memory.RecallTool` / `memory.SaveTool` registrations, add:

```go
	if err := reg.Register(memory.ListTool{Store: store}); err != nil {
		return err
	}
```

(Adapt to the surrounding error-handling style in `chat.go`.)

- [ ] **Step 3: Write the tool test**

Create `memory/recall_tool_test.go`:

```go
//go:build integration

package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListTool_Query(t *testing.T) {
	s, ctx := newMemStore(t)
	_, err := s.SaveMemories(ctx, []Candidate{
		{Kind: "agreement", Summary: "Berlin trip in May", DedupeKey: "berlin"},
		{Kind: "person_fact", Summary: "Anna likes tea", People: []string{"Anna"}, DedupeKey: "anna-tea"},
	}, "G", []string{"x"})
	require.NoError(t, err)

	out, err := ListTool{Store: s}.Execute(ctx, []byte(`{"query":"Berlin"}`))
	require.NoError(t, err)
	require.Contains(t, out, "Berlin trip in May")

	out, err = ListTool{Store: s}.Execute(ctx, []byte(`{"kind":"person_fact","status":"active"}`))
	require.NoError(t, err)
	require.Contains(t, out, "Anna likes tea")
}
```

- [ ] **Step 4: Build + test**

Run: `go build ./... && go test -tags integration ./memory/ -run TestListTool -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add memory/recall_tool.go memory/recall_tool_test.go cmd/darek/chat.go
git commit -m "feat(memory): memory.list chat recall tool"
```

---

## Task 9 — README + deployment + final verification

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Document the feature**

Add a "Memory" subsection under the existing WhatsApp / daily-digest documentation:

```markdown
### Cross-digest memory

When `memory.enabled: true` (and WhatsApp + OpenAI are configured), the daily
digest also extracts durable memories from recent WhatsApp conversation:

- **agreements** / **events** — plans the group made, with an optional date
- **person_fact** — durable facts about people in the chat

Memories are stored in the `memories` table and deduplicated by a stable key, so
re-mentioning a plan updates it rather than creating a duplicate. The email gains
a **Follow-ups** section (active agreements/events due soon, overdue, or recently
mentioned) and a **People** section (facts seen in the last few days). Nothing is
changed automatically — Darek never edits your calendar from these.

Recalled from chat via the `memory.list` agent tool. Extraction looks back at most
7 days and runs independently of the WhatsApp *summary* cursor, so a failure in
one never suppresses the other.
```

- [ ] **Step 2: Final verification**

Run: `go build ./... && make test && make lint && CGO_ENABLED=0 go build -o /tmp/darek-test ./cmd/darek`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs(readme): document cross-digest memory"
```

- [ ] **Step 4: Enable in deployment (separate repo) + smoke test**

In `homelab-k8s` (`values/darek.yaml`), add under the darek config:

```yaml
memory:
  enabled: true
```

Bump the image tag, let ArgoCD roll, then:

```bash
# trigger the cron manually
kubectl -n darek create job --from=cronjob/darek-daily-digest memory-test
kubectl -n darek logs -f job/memory-test        # expect: "info: memory: groups=… extracted=… saved=…"

# verify the email gained Follow-ups / People, then trigger again:
kubectl -n darek create job --from=cronjob/darek-daily-digest memory-test-2  # extraction is a no-op
```

Confirm in Postgres:

```sql
SELECT kind, status, summary, due_at, people, groups FROM memories ORDER BY last_seen_at DESC;
SELECT count(*) FROM whatsapp_messages WHERE memories_extracted_at IS NOT NULL;
```
</content>
