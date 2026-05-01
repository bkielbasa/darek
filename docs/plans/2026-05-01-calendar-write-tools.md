# Calendar Write Tools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add full CRUD on Google Calendar events via three new agent tools (`calendar.create_event`, `calendar.update_event`, `calendar.delete_event`), with type-level read-only/writable separation.

**Architecture:** A new `WritableCalendarSource` interface embeds the existing read-only `CalendarSource`. Only the Google source implements it; ICS stays read-only. `Sources` gains thin dispatch helpers (`Create`, `Update`, `Delete`) that type-assert and return a sentinel error for read-only calendars. Three new tools sit alongside `ListEventsTool` in `tools/calendar/tool.go`. Update uses PATCH semantics via two-pass JSON decoding (presence detection over `map[string]json.RawMessage`).

**Tech Stack:** `google.golang.org/api/calendar/v3` (now `CalendarEventsScope`), existing `obs.Dep` tracing wrapper, `testify/require` for tests.

**Spec:** `docs/specs/2026-05-01-calendar-write-tools-design.md`.

**Out of scope:** ICS writes, recurrence (RRULE), series-level recurring edits, dedup, live integration tests against real Google.

---

## File Map

| Path | Responsibility |
|---|---|
| `tools/calendar/calendar.go` | (modify) add `ErrReadOnly`, `WritableCalendarSource`, `NewEvent`, `EventPatch`, `Sources.Create/Update/Delete`. |
| `tools/calendar/calendar_test.go` | (modify) add tests for the three dispatch helpers. |
| `tools/calendar/tool.go` | (modify) add `CreateEventTool`, `UpdateEventTool`, `DeleteEventTool` and shared validation helpers. |
| `tools/calendar/tool_test.go` | (modify) add tests for the three new tools using a writable fake source. |
| `tools/calendar/google/google.go` | (modify) widen scope, add `(*Source).CreateEvent/UpdateEvent/DeleteEvent` and conversion helpers. |
| `tools/calendar/google/google_test.go` | (create) tests for `NewEvent`/`EventPatch` → Google API type conversion. |
| `cmd/darek/chat.go` | (modify) register the three new tools when at least one writable calendar exists. |

---

## Task 1 — Sentinel error and writable interface

**Files:**
- Modify: `tools/calendar/calendar.go` (add at top after imports)

- [ ] **Step 1: Edit `tools/calendar/calendar.go` — add types**

Add this block after the existing `Event` struct and before `CalendarSource`:

```go
var ErrReadOnly = errors.New("calendar is read-only")

// NewEvent is the input shape for creating a calendar event.
type NewEvent struct {
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	AllDay      bool
	Attendees   []string // emails
	SendInvites bool
}

// EventPatch carries PATCH-style updates: only non-nil pointer fields are applied.
// For Attendees: nil means "no change", a non-nil pointer to a slice (including empty)
// replaces the full attendee list.
type EventPatch struct {
	Summary     *string
	Description *string
	Location    *string
	Start       *time.Time
	End         *time.Time
	AllDay      *bool
	Attendees   *[]string
	SendInvites bool
}

// WritableCalendarSource is implemented by sources that support mutations.
// Read-only sources (e.g. iCal feeds) don't implement it.
type WritableCalendarSource interface {
	CalendarSource
	CreateEvent(ctx context.Context, in NewEvent) (Event, error)
	UpdateEvent(ctx context.Context, uid string, patch EventPatch) (Event, error)
	DeleteEvent(ctx context.Context, uid string, sendInvites bool) error
}
```

Add `"errors"` to the import list (it's not currently imported).

- [ ] **Step 2: Build to verify**

Run: `go build ./tools/calendar/...`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add tools/calendar/calendar.go
git commit -m "feat(calendar): add WritableCalendarSource interface and patch types"
```

---

## Task 2 — Sources dispatch helpers (TDD)

**Files:**
- Modify: `tools/calendar/calendar_test.go`
- Modify: `tools/calendar/calendar.go`

- [ ] **Step 1: Extend the existing `fakeSrc` to support writes (in `calendar_test.go`)**

Append after the existing `fakeSrc` definition:

```go
// fakeWritableSrc embeds fakeSrc behaviour and records write calls.
type fakeWritableSrc struct {
	fakeSrc
	created  []NewEvent
	updates  []struct {
		UID   string
		Patch EventPatch
	}
	deletes  []struct {
		UID         string
		SendInvites bool
	}
	createErr error
	updateErr error
	deleteErr error
}

func (f *fakeWritableSrc) CreateEvent(_ context.Context, in NewEvent) (Event, error) {
	if f.createErr != nil {
		return Event{}, f.createErr
	}
	f.created = append(f.created, in)
	return Event{Calendar: f.name, UID: "new-uid", Summary: in.Summary, Start: in.Start, End: in.End}, nil
}

func (f *fakeWritableSrc) UpdateEvent(_ context.Context, uid string, p EventPatch) (Event, error) {
	if f.updateErr != nil {
		return Event{}, f.updateErr
	}
	f.updates = append(f.updates, struct {
		UID   string
		Patch EventPatch
	}{uid, p})
	return Event{Calendar: f.name, UID: uid, Summary: "updated"}, nil
}

func (f *fakeWritableSrc) DeleteEvent(_ context.Context, uid string, sendInvites bool) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletes = append(f.deletes, struct {
		UID         string
		SendInvites bool
	}{uid, sendInvites})
	return nil
}
```

- [ ] **Step 2: Add failing dispatch-helper tests in `calendar_test.go`**

Append:

```go
func TestSources_Create_RoutesToWritableSource(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	in := NewEvent{Summary: "hi", Start: time.Now(), End: time.Now().Add(time.Hour)}
	got, err := s.Create(context.Background(), "work", in)
	require.NoError(t, err)
	require.Equal(t, "new-uid", got.UID)
	require.Len(t, w.created, 1)
	require.Equal(t, "hi", w.created[0].Summary)
}

func TestSources_Create_ReadOnlyCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "feed"}))
	_, err := s.Create(context.Background(), "feed", NewEvent{Summary: "x"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrReadOnly)
	require.Contains(t, err.Error(), `"feed"`)
}

func TestSources_Create_UnknownCalendar(t *testing.T) {
	s := NewSources()
	_, err := s.Create(context.Background(), "nope", NewEvent{Summary: "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown calendar")
}

func TestSources_Update_RoutesToWritableSource(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	summary := "renamed"
	patch := EventPatch{Summary: &summary}
	got, err := s.Update(context.Background(), "work", "abc", patch)
	require.NoError(t, err)
	require.Equal(t, "abc", got.UID)
	require.Len(t, w.updates, 1)
	require.Equal(t, "abc", w.updates[0].UID)
	require.Equal(t, &summary, w.updates[0].Patch.Summary)
}

func TestSources_Update_ReadOnlyCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "feed"}))
	_, err := s.Update(context.Background(), "feed", "abc", EventPatch{})
	require.ErrorIs(t, err, ErrReadOnly)
}

func TestSources_Delete_RoutesToWritableSource(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	require.NoError(t, s.Delete(context.Background(), "work", "abc", true))
	require.Len(t, w.deletes, 1)
	require.Equal(t, "abc", w.deletes[0].UID)
	require.True(t, w.deletes[0].SendInvites)
}

func TestSources_Delete_ReadOnlyCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "feed"}))
	err := s.Delete(context.Background(), "feed", "abc", false)
	require.ErrorIs(t, err, ErrReadOnly)
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./tools/calendar/ -run TestSources_Create_RoutesToWritableSource -v`
Expected: FAIL — `s.Create undefined`.

- [ ] **Step 4: Implement the dispatch helpers in `calendar.go`**

Append to `tools/calendar/calendar.go` (after `ListEvents`):

```go
// Create resolves the calendar nickname to a writable source and creates the event.
// Returns ErrReadOnly (wrapped with the nickname) if the source isn't writable.
func (s *Sources) Create(ctx context.Context, calendar string, in NewEvent) (Event, error) {
	w, err := s.writable(calendar)
	if err != nil {
		return Event{}, err
	}
	return w.CreateEvent(ctx, in)
}

// Update resolves the calendar nickname to a writable source and applies the patch.
func (s *Sources) Update(ctx context.Context, calendar, uid string, patch EventPatch) (Event, error) {
	w, err := s.writable(calendar)
	if err != nil {
		return Event{}, err
	}
	return w.UpdateEvent(ctx, uid, patch)
}

// Delete resolves the calendar nickname to a writable source and deletes the event.
func (s *Sources) Delete(ctx context.Context, calendar, uid string, sendInvites bool) error {
	w, err := s.writable(calendar)
	if err != nil {
		return err
	}
	return w.DeleteEvent(ctx, uid, sendInvites)
}

// writable looks up `calendar` and returns it as a WritableCalendarSource, or
// an error wrapping ErrReadOnly / unknown-calendar.
func (s *Sources) writable(calendar string) (WritableCalendarSource, error) {
	s.mu.RLock()
	src, ok := s.bynm[calendar]
	if !ok {
		names := s.namesUnlocked()
		s.mu.RUnlock()
		return nil, fmt.Errorf("unknown calendar %q (have: %v)", calendar, names)
	}
	s.mu.RUnlock()
	w, ok := src.(WritableCalendarSource)
	if !ok {
		return nil, fmt.Errorf("calendar %q: %w", calendar, ErrReadOnly)
	}
	return w, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./tools/calendar/ -v`
Expected: all dispatch-helper tests PASS, existing tests still PASS.

- [ ] **Step 6: Commit**

```bash
git add tools/calendar/calendar.go tools/calendar/calendar_test.go
git commit -m "feat(calendar): add Sources dispatch helpers for writable sources"
```

---

## Task 3 — `calendar.create_event` tool (TDD)

**Files:**
- Modify: `tools/calendar/tool_test.go`
- Modify: `tools/calendar/tool.go`

- [ ] **Step 1: Write failing tests in `tool_test.go`**

Append:

```go
func TestCreateEventTool_HappyPath(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{
		"calendar":"work",
		"summary":"Lunch",
		"start":"2026-05-02T12:00:00+02:00",
		"end":"2026-05-02T13:00:00+02:00",
		"location":"Cafe",
		"attendees":["a@example.com"],
		"send_invites":true
	}`)
	out, err := CreateEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Contains(t, out, "[work]")
	require.Contains(t, out, "Lunch")
	require.Contains(t, out, "uid: new-uid")

	require.Len(t, w.created, 1)
	got := w.created[0]
	require.Equal(t, "Lunch", got.Summary)
	require.Equal(t, "Cafe", got.Location)
	require.Equal(t, []string{"a@example.com"}, got.Attendees)
	require.True(t, got.SendInvites)
	require.False(t, got.AllDay)
}

func TestCreateEventTool_DefaultEndTimedIsOneHour(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","summary":"Quick","start":"2026-05-02T15:00:00Z"}`)
	_, err := CreateEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Len(t, w.created, 1)
	require.Equal(t, time.Hour, w.created[0].End.Sub(w.created[0].Start))
}

func TestCreateEventTool_AllDayDefaultEndIsOneDay(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","summary":"Holiday","start":"2026-05-02","all_day":true}`)
	_, err := CreateEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Len(t, w.created, 1)
	require.True(t, w.created[0].AllDay)
	require.Equal(t, 24*time.Hour, w.created[0].End.Sub(w.created[0].Start))
}

func TestCreateEventTool_ReadOnlyCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "feed"}))
	args := json.RawMessage(`{"calendar":"feed","summary":"x","start":"2026-05-02T15:00:00Z"}`)
	_, err := CreateEventTool{Sources: s}.Execute(context.Background(), args)
	require.Error(t, err)
	require.Contains(t, err.Error(), "read-only")
}

func TestCreateEventTool_ValidationErrors(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	cases := map[string]string{
		"empty summary":       `{"calendar":"work","summary":"","start":"2026-05-02T15:00:00Z"}`,
		"missing summary":     `{"calendar":"work","start":"2026-05-02T15:00:00Z"}`,
		"missing calendar":    `{"summary":"x","start":"2026-05-02T15:00:00Z"}`,
		"missing start":       `{"calendar":"work","summary":"x"}`,
		"end before start":    `{"calendar":"work","summary":"x","start":"2026-05-02T15:00:00Z","end":"2026-05-02T14:00:00Z"}`,
		"bad rfc3339":         `{"calendar":"work","summary":"x","start":"not-a-time"}`,
		"all_day with time":   `{"calendar":"work","summary":"x","start":"2026-05-02T15:00:00Z","all_day":true}`,
		"non-all-day with date": `{"calendar":"work","summary":"x","start":"2026-05-02"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := CreateEventTool{Sources: s}.Execute(context.Background(), json.RawMessage(body))
			require.Error(t, err)
			require.Empty(t, w.created)
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./tools/calendar/ -run TestCreateEventTool -v`
Expected: FAIL — `CreateEventTool undefined`.

- [ ] **Step 3: Implement `CreateEventTool` and shared parse helpers in `tool.go`**

Append to `tools/calendar/tool.go`:

```go
// parseStart parses a "start" or "end" string given the all_day flag.
// On all_day=true, it accepts YYYY-MM-DD; otherwise RFC3339.
// Returns the parsed time and an error describing the problem.
func parseEventTime(s string, allDay bool, field string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("%s: required", field)
	}
	if allDay {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s: all_day requires YYYY-MM-DD, got %q", field, s)
		}
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", field, err)
	}
	return t, nil
}

type CreateEventTool struct {
	Sources *Sources
}

func (CreateEventTool) Name() string { return "calendar.create_event" }
func (CreateEventTool) Description() string {
	return "Create a calendar event on a writable calendar. Returns the created event with its UID."
}
func (CreateEventTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"calendar":{"type":"string","description":"nickname of a writable calendar"},
			"summary":{"type":"string"},
			"start":{"type":"string","description":"RFC3339 datetime, or YYYY-MM-DD if all_day=true"},
			"end":{"type":"string","description":"same format; defaults to start+1h (timed) or start+1 day (all_day)"},
			"all_day":{"type":"boolean","default":false},
			"description":{"type":"string"},
			"location":{"type":"string"},
			"attendees":{"type":"array","items":{"type":"string"}},
			"send_invites":{"type":"boolean","default":false}
		},
		"required":["calendar","summary","start"]
	}`)
}

func (t CreateEventTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Calendar    string   `json:"calendar"`
		Summary     string   `json:"summary"`
		Start       string   `json:"start"`
		End         string   `json:"end"`
		AllDay      bool     `json:"all_day"`
		Description string   `json:"description"`
		Location    string   `json:"location"`
		Attendees   []string `json:"attendees"`
		SendInvites bool     `json:"send_invites"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	if p.Calendar == "" {
		return "", fmt.Errorf("calendar: required")
	}
	if p.Summary == "" {
		return "", fmt.Errorf("summary: required")
	}
	start, err := parseEventTime(p.Start, p.AllDay, "start")
	if err != nil {
		return "", err
	}
	var end time.Time
	if p.End == "" {
		if p.AllDay {
			end = start.Add(24 * time.Hour)
		} else {
			end = start.Add(time.Hour)
		}
	} else {
		end, err = parseEventTime(p.End, p.AllDay, "end")
		if err != nil {
			return "", err
		}
	}
	if end.Before(start) {
		return "", fmt.Errorf("end must not be before start")
	}
	in := NewEvent{
		Summary:     p.Summary,
		Description: p.Description,
		Location:    p.Location,
		Start:       start,
		End:         end,
		AllDay:      p.AllDay,
		Attendees:   p.Attendees,
		SendInvites: p.SendInvites,
	}
	ev, err := t.Sources.Create(ctx, p.Calendar, in)
	if err != nil {
		return "", err
	}
	return formatCreatedOrUpdated(ev), nil
}

// formatCreatedOrUpdated renders an event in the same shape ListEventsTool uses,
// followed by a "uid: ..." line so the agent can address it later.
func formatCreatedOrUpdated(ev Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s — %s",
		ev.Calendar, ev.Start.Format(time.RFC3339), ev.Summary)
	if ev.Location != "" {
		fmt.Fprintf(&b, " @ %s", ev.Location)
	}
	fmt.Fprintf(&b, "\nuid: %s", ev.UID)
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./tools/calendar/ -run TestCreateEventTool -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/calendar/tool.go tools/calendar/tool_test.go
git commit -m "feat(calendar): add calendar.create_event tool"
```

---

## Task 4 — `calendar.update_event` tool with PATCH semantics (TDD)

**Files:**
- Modify: `tools/calendar/tool_test.go`
- Modify: `tools/calendar/tool.go`

- [ ] **Step 1: Write failing tests in `tool_test.go`**

Append:

```go
func TestUpdateEventTool_PartialPatch(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","uid":"abc","summary":"renamed"}`)
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Len(t, w.updates, 1)
	require.Equal(t, "abc", w.updates[0].UID)
	require.NotNil(t, w.updates[0].Patch.Summary)
	require.Equal(t, "renamed", *w.updates[0].Patch.Summary)
	require.Nil(t, w.updates[0].Patch.Description)
	require.Nil(t, w.updates[0].Patch.Attendees)
	require.Nil(t, w.updates[0].Patch.Start)
}

func TestUpdateEventTool_AttendeesPresenceClearVsAbsent(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	// Absent — no change
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc","summary":"x"}`))
	require.NoError(t, err)
	require.Nil(t, w.updates[0].Patch.Attendees)

	// Present empty — clear all
	_, err = UpdateEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc","attendees":[]}`))
	require.NoError(t, err)
	require.NotNil(t, w.updates[1].Patch.Attendees)
	require.Empty(t, *w.updates[1].Patch.Attendees)

	// Present with values — replace
	_, err = UpdateEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc","attendees":["a@example.com"]}`))
	require.NoError(t, err)
	require.NotNil(t, w.updates[2].Patch.Attendees)
	require.Equal(t, []string{"a@example.com"}, *w.updates[2].Patch.Attendees)
}

func TestUpdateEventTool_NoFields(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "no fields to update")
	require.Empty(t, w.updates)
}

func TestUpdateEventTool_StartEndBothPresentValidates(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	bad := json.RawMessage(`{"calendar":"work","uid":"abc","start":"2026-05-02T15:00:00Z","end":"2026-05-02T14:00:00Z"}`)
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(), bad)
	require.Error(t, err)
	require.Contains(t, err.Error(), "end must not be before start")
}

func TestUpdateEventTool_StartOnlyPatchSkipsCompare(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","uid":"abc","start":"2026-05-02T15:00:00Z"}`)
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Len(t, w.updates, 1)
	require.NotNil(t, w.updates[0].Patch.Start)
	require.Nil(t, w.updates[0].Patch.End)
}

func TestUpdateEventTool_ReadOnlyCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "feed"}))
	args := json.RawMessage(`{"calendar":"feed","uid":"abc","summary":"x"}`)
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(), args)
	require.Error(t, err)
	require.Contains(t, err.Error(), "read-only")
}

func TestUpdateEventTool_SendInvitesRoundTrips(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc","summary":"x","send_invites":true}`))
	require.NoError(t, err)
	require.True(t, w.updates[0].Patch.SendInvites)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./tools/calendar/ -run TestUpdateEventTool -v`
Expected: FAIL — `UpdateEventTool undefined`.

- [ ] **Step 3: Implement `UpdateEventTool` in `tool.go`**

Append to `tools/calendar/tool.go`:

```go
type UpdateEventTool struct {
	Sources *Sources
}

func (UpdateEventTool) Name() string { return "calendar.update_event" }
func (UpdateEventTool) Description() string {
	return "Update a calendar event by UID. PATCH semantics: only fields present in the call are changed."
}
func (UpdateEventTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"calendar":{"type":"string"},
			"uid":{"type":"string","description":"from list_events"},
			"summary":{"type":"string"},
			"start":{"type":"string"},
			"end":{"type":"string"},
			"all_day":{"type":"boolean"},
			"description":{"type":"string"},
			"location":{"type":"string"},
			"attendees":{"type":"array","items":{"type":"string"},"description":"replaces the full attendee list"},
			"send_invites":{"type":"boolean","default":false}
		},
		"required":["calendar","uid"]
	}`)
}

func (t UpdateEventTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// First pass: detect which keys are present.
	var raw map[string]json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	calendar := requireString(raw, "calendar")
	uid := requireString(raw, "uid")
	if calendar == "" {
		return "", fmt.Errorf("calendar: required")
	}
	if uid == "" {
		return "", fmt.Errorf("uid: required")
	}

	// Determine all_day for time parsing. If the patch sets it, use the patched value.
	allDay := false
	if v, ok := raw["all_day"]; ok {
		if err := json.Unmarshal(v, &allDay); err != nil {
			return "", fmt.Errorf("all_day: %w", err)
		}
	}

	patch := EventPatch{}
	patchFields := 0
	if v, ok := raw["summary"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return "", fmt.Errorf("summary: %w", err)
		}
		patch.Summary = &s
		patchFields++
	}
	if v, ok := raw["description"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return "", fmt.Errorf("description: %w", err)
		}
		patch.Description = &s
		patchFields++
	}
	if v, ok := raw["location"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return "", fmt.Errorf("location: %w", err)
		}
		patch.Location = &s
		patchFields++
	}
	if v, ok := raw["start"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return "", fmt.Errorf("start: %w", err)
		}
		ts, err := parseEventTime(s, allDay, "start")
		if err != nil {
			return "", err
		}
		patch.Start = &ts
		patchFields++
	}
	if v, ok := raw["end"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return "", fmt.Errorf("end: %w", err)
		}
		ts, err := parseEventTime(s, allDay, "end")
		if err != nil {
			return "", err
		}
		patch.End = &ts
		patchFields++
	}
	if _, ok := raw["all_day"]; ok {
		patch.AllDay = &allDay
		patchFields++
	}
	if v, ok := raw["attendees"]; ok {
		var atts []string
		if err := json.Unmarshal(v, &atts); err != nil {
			return "", fmt.Errorf("attendees: %w", err)
		}
		patch.Attendees = &atts
		patchFields++
	}
	if v, ok := raw["send_invites"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			return "", fmt.Errorf("send_invites: %w", err)
		}
		patch.SendInvites = b
		// send_invites alone is not considered a "patch field" — it's just a notification flag.
	}

	if patchFields == 0 {
		return "", fmt.Errorf("no fields to update")
	}
	if patch.Start != nil && patch.End != nil && patch.End.Before(*patch.Start) {
		return "", fmt.Errorf("end must not be before start")
	}

	ev, err := t.Sources.Update(ctx, calendar, uid, patch)
	if err != nil {
		return "", err
	}
	return formatCreatedOrUpdated(ev), nil
}

// requireString decodes raw[key] into a string, returning "" if absent or on decode error.
// The caller is responsible for handling required-ness (so that distinct error messages can be returned).
func requireString(raw map[string]json.RawMessage, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./tools/calendar/ -run TestUpdateEventTool -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/calendar/tool.go tools/calendar/tool_test.go
git commit -m "feat(calendar): add calendar.update_event tool with PATCH semantics"
```

---

## Task 5 — `calendar.delete_event` tool (TDD)

**Files:**
- Modify: `tools/calendar/tool_test.go`
- Modify: `tools/calendar/tool.go`

- [ ] **Step 1: Write failing tests in `tool_test.go`**

Append:

```go
func TestDeleteEventTool_HappyPath(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","uid":"abc","send_invites":true}`)
	out, err := DeleteEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Equal(t, `deleted: abc from work`, out)

	require.Len(t, w.deletes, 1)
	require.Equal(t, "abc", w.deletes[0].UID)
	require.True(t, w.deletes[0].SendInvites)
}

func TestDeleteEventTool_DefaultSendInvitesFalse(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	_, err := DeleteEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc"}`))
	require.NoError(t, err)
	require.False(t, w.deletes[0].SendInvites)
}

func TestDeleteEventTool_RequiredFields(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	for name, body := range map[string]string{
		"missing calendar": `{"uid":"abc"}`,
		"missing uid":      `{"calendar":"work"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DeleteEventTool{Sources: s}.Execute(context.Background(), json.RawMessage(body))
			require.Error(t, err)
		})
	}
	require.Empty(t, w.deletes)
}

func TestDeleteEventTool_ReadOnlyCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "feed"}))
	_, err := DeleteEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"feed","uid":"abc"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "read-only")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./tools/calendar/ -run TestDeleteEventTool -v`
Expected: FAIL — `DeleteEventTool undefined`.

- [ ] **Step 3: Implement `DeleteEventTool` in `tool.go`**

Append to `tools/calendar/tool.go`:

```go
type DeleteEventTool struct {
	Sources *Sources
}

func (DeleteEventTool) Name() string { return "calendar.delete_event" }
func (DeleteEventTool) Description() string {
	return "Delete a calendar event by UID."
}
func (DeleteEventTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"calendar":{"type":"string"},
			"uid":{"type":"string"},
			"send_invites":{"type":"boolean","default":false}
		},
		"required":["calendar","uid"]
	}`)
}

func (t DeleteEventTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Calendar    string `json:"calendar"`
		UID         string `json:"uid"`
		SendInvites bool   `json:"send_invites"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	if p.Calendar == "" {
		return "", fmt.Errorf("calendar: required")
	}
	if p.UID == "" {
		return "", fmt.Errorf("uid: required")
	}
	if err := t.Sources.Delete(ctx, p.Calendar, p.UID, p.SendInvites); err != nil {
		return "", err
	}
	return fmt.Sprintf("deleted: %s from %s", p.UID, p.Calendar), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./tools/calendar/ -v`
Expected: all calendar package tests PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/calendar/tool.go tools/calendar/tool_test.go
git commit -m "feat(calendar): add calendar.delete_event tool"
```

---

## Task 6 — Widen Google OAuth scope

**Files:**
- Modify: `tools/calendar/google/google.go:18`

- [ ] **Step 1: Edit the scope constant**

Change line 18 from:

```go
const Scope = calsvc.CalendarReadonlyScope
```

to:

```go
const Scope = calsvc.CalendarEventsScope
```

This propagates through `Config()` (line 27) automatically. The `darek calendar refresh-token <nickname>` subcommand re-uses `Config()`, so re-auth picks up the new scope without further changes.

- [ ] **Step 2: Build to verify**

Run: `go build ./tools/calendar/google/...`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add tools/calendar/google/google.go
git commit -m "feat(calendar/google): widen OAuth scope to CalendarEventsScope

Existing tokens become insufficient for write ops; operators must re-run
'darek calendar refresh-token <nickname>' once per Google calendar to mint
a new token covering read+write event access."
```

---

## Task 7 — Google source: build/parse helpers (TDD)

**Files:**
- Create: `tools/calendar/google/google_test.go`
- Modify: `tools/calendar/google/google.go`

- [ ] **Step 1: Write failing tests in `google_test.go`**

```go
package google

import (
	"testing"
	"time"

	"darek/tools/calendar"

	"github.com/stretchr/testify/require"
	calsvc "google.golang.org/api/calendar/v3"
)

func TestBuildAPIEvent_Timed(t *testing.T) {
	start := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	in := calendar.NewEvent{
		Summary:     "Lunch",
		Description: "with team",
		Location:    "Cafe",
		Start:       start,
		End:         end,
		Attendees:   []string{"a@example.com", "b@example.com"},
	}
	got := buildAPIEvent(in)
	require.Equal(t, "Lunch", got.Summary)
	require.Equal(t, "with team", got.Description)
	require.Equal(t, "Cafe", got.Location)
	require.NotNil(t, got.Start)
	require.Equal(t, start.Format(time.RFC3339), got.Start.DateTime)
	require.Empty(t, got.Start.Date)
	require.Equal(t, end.Format(time.RFC3339), got.End.DateTime)
	require.Len(t, got.Attendees, 2)
	require.Equal(t, "a@example.com", got.Attendees[0].Email)
}

func TestBuildAPIEvent_AllDay(t *testing.T) {
	start := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	in := calendar.NewEvent{Summary: "Holiday", Start: start, End: end, AllDay: true}
	got := buildAPIEvent(in)
	require.Equal(t, "2026-05-02", got.Start.Date)
	require.Empty(t, got.Start.DateTime)
	require.Equal(t, "2026-05-03", got.End.Date)
}

func TestBuildAPIPatch_OnlyPresentFields(t *testing.T) {
	summary := "renamed"
	patch := calendar.EventPatch{Summary: &summary}
	got := buildAPIPatch(patch)
	require.Equal(t, "renamed", got.Summary)
	require.Nil(t, got.Start)
	require.Nil(t, got.End)
	require.Nil(t, got.Attendees)
	// ForceSendFields must include "Summary" so an empty string would clear it; here
	// the value is non-empty but presence-tracking still applies.
	require.Contains(t, got.ForceSendFields, "Summary")
}

func TestBuildAPIPatch_AttendeesCleared(t *testing.T) {
	empty := []string{}
	patch := calendar.EventPatch{Attendees: &empty}
	got := buildAPIPatch(patch)
	require.NotNil(t, got.Attendees)
	require.Empty(t, got.Attendees)
	// ForceSendFields ensures the empty slice is sent (not omitted by omitempty).
	require.Contains(t, got.ForceSendFields, "Attendees")
}

func TestBuildAPIPatch_TimedStart(t *testing.T) {
	start := time.Date(2026, 5, 2, 15, 0, 0, 0, time.UTC)
	allDay := false
	patch := calendar.EventPatch{Start: &start, AllDay: &allDay}
	got := buildAPIPatch(patch)
	require.NotNil(t, got.Start)
	require.Equal(t, start.Format(time.RFC3339), got.Start.DateTime)
	require.Empty(t, got.Start.Date)
}

func TestBuildAPIPatch_AllDayStart(t *testing.T) {
	start := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	allDay := true
	patch := calendar.EventPatch{Start: &start, AllDay: &allDay}
	got := buildAPIPatch(patch)
	require.NotNil(t, got.Start)
	require.Equal(t, "2026-05-02", got.Start.Date)
	require.Empty(t, got.Start.DateTime)
}

func TestSendUpdates(t *testing.T) {
	require.Equal(t, "all", sendUpdates(true))
	require.Equal(t, "none", sendUpdates(false))
}

func TestConvertCreatedEvent(t *testing.T) {
	api := &calsvc.Event{
		Id:      "abc123",
		Summary: "Lunch",
		Start:   &calsvc.EventDateTime{DateTime: "2026-05-02T12:00:00Z"},
		End:     &calsvc.EventDateTime{DateTime: "2026-05-02T13:00:00Z"},
	}
	ev, ok := convert("work", api)
	require.True(t, ok)
	require.Equal(t, "abc123", ev.UID)
	require.Equal(t, "work", ev.Calendar)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./tools/calendar/google/ -v`
Expected: FAIL — `buildAPIEvent`, `buildAPIPatch`, `sendUpdates` undefined.

- [ ] **Step 3: Add helpers to `google.go`**

Append to `tools/calendar/google/google.go`:

```go
// buildAPIEvent converts a NewEvent into the Google Calendar API shape.
func buildAPIEvent(in calendar.NewEvent) *calsvc.Event {
	out := &calsvc.Event{
		Summary:     in.Summary,
		Description: in.Description,
		Location:    in.Location,
		Start:       eventDateTime(in.Start, in.AllDay),
		End:         eventDateTime(in.End, in.AllDay),
	}
	if len(in.Attendees) > 0 {
		out.Attendees = make([]*calsvc.EventAttendee, 0, len(in.Attendees))
		for _, e := range in.Attendees {
			out.Attendees = append(out.Attendees, &calsvc.EventAttendee{Email: e})
		}
	}
	return out
}

// buildAPIPatch converts an EventPatch into a partial Google Calendar API event,
// using ForceSendFields so that empty values (e.g. cleared description, empty
// attendee list) are actually sent rather than dropped by omitempty.
func buildAPIPatch(p calendar.EventPatch) *calsvc.Event {
	out := &calsvc.Event{}
	allDay := false
	if p.AllDay != nil {
		allDay = *p.AllDay
	}
	if p.Summary != nil {
		out.Summary = *p.Summary
		out.ForceSendFields = append(out.ForceSendFields, "Summary")
	}
	if p.Description != nil {
		out.Description = *p.Description
		out.ForceSendFields = append(out.ForceSendFields, "Description")
	}
	if p.Location != nil {
		out.Location = *p.Location
		out.ForceSendFields = append(out.ForceSendFields, "Location")
	}
	if p.Start != nil {
		out.Start = eventDateTime(*p.Start, allDay)
	}
	if p.End != nil {
		out.End = eventDateTime(*p.End, allDay)
	}
	if p.Attendees != nil {
		out.Attendees = make([]*calsvc.EventAttendee, 0, len(*p.Attendees))
		for _, e := range *p.Attendees {
			out.Attendees = append(out.Attendees, &calsvc.EventAttendee{Email: e})
		}
		out.ForceSendFields = append(out.ForceSendFields, "Attendees")
	}
	return out
}

// eventDateTime renders a time as the right Google API date-or-datetime shape.
func eventDateTime(t time.Time, allDay bool) *calsvc.EventDateTime {
	if allDay {
		return &calsvc.EventDateTime{Date: t.Format("2006-01-02")}
	}
	return &calsvc.EventDateTime{DateTime: t.Format(time.RFC3339)}
}

// sendUpdates maps the bool flag to Google's enum for the sendUpdates query param.
func sendUpdates(send bool) string {
	if send {
		return "all"
	}
	return "none"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./tools/calendar/google/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/calendar/google/google.go tools/calendar/google/google_test.go
git commit -m "feat(calendar/google): add API event/patch builders and conversion helpers"
```

---

## Task 8 — Google source: implement Create/Update/Delete

**Files:**
- Modify: `tools/calendar/google/google.go`

This task wires the helpers from Task 7 into `*Source` methods that call the live Google API. There are no unit tests for the API plumbing itself (no live calls; `obs.Dep` is opaque to caller logic). The helpers are already covered.

- [ ] **Step 1: Add `clientService` helper**

Append a small helper that mirrors what `ListEvents` does inline (token load + traced HTTP client + service):

```go
// clientService loads the OAuth token and builds a traced calendar service.
// Returns an error if the token can't be loaded or the service can't be built.
func (s *Source) clientService(ctx context.Context) (*calsvc.Service, error) {
	tok, err := s.store.Load(s.nickname)
	if err != nil {
		return nil, fmt.Errorf("load token: %w", err)
	}
	httpClient := s.cfg.Client(ctx, tok)
	httpClient.Transport = otelhttp.NewTransport(httpClient.Transport)
	svc, err := calsvc.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("calendar svc: %w", err)
	}
	return svc, nil
}
```

(Optional: refactor `ListEvents` to use this helper. Not required for this plan — leaving the existing inline code as-is keeps the diff focused.)

- [ ] **Step 2: Add `CreateEvent`**

Append:

```go
func (s *Source) CreateEvent(ctx context.Context, in calendar.NewEvent) (calendar.Event, error) {
	svc, err := s.clientService(ctx)
	if err != nil {
		return calendar.Event{}, err
	}
	apiEvent := buildAPIEvent(in)
	call := svc.Events.Insert(s.calID, apiEvent).
		SendUpdates(sendUpdates(in.SendInvites)).
		Context(ctx)
	var res *calsvc.Event
	if err := obs.Dep(ctx, "google_calendar", "create_event", func(ctx context.Context) error {
		var err error
		res, err = call.Do()
		return err
	}); err != nil {
		return calendar.Event{}, fmt.Errorf("events.insert: %w", err)
	}
	ev, ok := convert(s.nickname, res)
	if !ok {
		return calendar.Event{}, fmt.Errorf("events.insert: unparseable response")
	}
	return ev, nil
}
```

- [ ] **Step 3: Add `UpdateEvent`**

```go
func (s *Source) UpdateEvent(ctx context.Context, uid string, p calendar.EventPatch) (calendar.Event, error) {
	svc, err := s.clientService(ctx)
	if err != nil {
		return calendar.Event{}, err
	}
	apiPatch := buildAPIPatch(p)
	call := svc.Events.Patch(s.calID, uid, apiPatch).
		SendUpdates(sendUpdates(p.SendInvites)).
		Context(ctx)
	var res *calsvc.Event
	if err := obs.Dep(ctx, "google_calendar", "update_event", func(ctx context.Context) error {
		var err error
		res, err = call.Do()
		return err
	}); err != nil {
		return calendar.Event{}, fmt.Errorf("events.patch: %w", err)
	}
	ev, ok := convert(s.nickname, res)
	if !ok {
		return calendar.Event{}, fmt.Errorf("events.patch: unparseable response")
	}
	return ev, nil
}
```

- [ ] **Step 4: Add `DeleteEvent`**

```go
func (s *Source) DeleteEvent(ctx context.Context, uid string, sendInvites bool) error {
	svc, err := s.clientService(ctx)
	if err != nil {
		return err
	}
	call := svc.Events.Delete(s.calID, uid).
		SendUpdates(sendUpdates(sendInvites)).
		Context(ctx)
	if err := obs.Dep(ctx, "google_calendar", "delete_event", func(ctx context.Context) error {
		return call.Do()
	}); err != nil {
		return fmt.Errorf("events.delete: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Build to verify the writable-source contract is satisfied**

Run: `go build ./tools/calendar/...`
Expected: clean build. The compiler enforces that `*Source` now satisfies `calendar.WritableCalendarSource`.

- [ ] **Step 6: Add a compile-time assertion**

At the bottom of `google.go`, add:

```go
// Compile-time check that *Source implements WritableCalendarSource.
var _ calendar.WritableCalendarSource = (*Source)(nil)
```

- [ ] **Step 7: Build and run all tests**

Run: `go build ./... && go test ./tools/calendar/...`
Expected: clean build, all tests pass.

- [ ] **Step 8: Commit**

```bash
git add tools/calendar/google/google.go
git commit -m "feat(calendar/google): implement Create/Update/Delete event ops"
```

---

## Task 9 — Register the new tools in chat.go

**Files:**
- Modify: `cmd/darek/chat.go:130-132`

- [ ] **Step 1: Edit the registration block**

Replace:

```go
		if len(srcs.Names()) > 0 {
			if err := reg.Register(calendar.ListEventsTool{Sources: srcs}); err != nil {
				return err
			}
		}
```

with:

```go
		if len(srcs.Names()) > 0 {
			if err := reg.Register(calendar.ListEventsTool{Sources: srcs}); err != nil {
				return err
			}
			if err := reg.Register(calendar.CreateEventTool{Sources: srcs}); err != nil {
				return err
			}
			if err := reg.Register(calendar.UpdateEventTool{Sources: srcs}); err != nil {
				return err
			}
			if err := reg.Register(calendar.DeleteEventTool{Sources: srcs}); err != nil {
				return err
			}
		}
```

The write tools are registered unconditionally (alongside `ListEventsTool`). They surface a clear `read-only` error at call time if the agent targets an iCal nickname, rather than the model silently being unaware of write capability when at least one Google source is present.

- [ ] **Step 2: Build to verify**

Run: `go build ./cmd/darek`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add cmd/darek/chat.go
git commit -m "feat(cmd/darek): register calendar create/update/delete tools"
```

---

## Task 10 — Final verification

- [ ] **Step 1: Run all tests and lint**

Run: `make test && make lint`
Expected: all tests pass, `go vet` clean.

- [ ] **Step 2: Run integration tests**

Run: `make test-integration`
Expected: pass (no integration tests added in this plan; existing suite must still pass).

- [ ] **Step 3: Manual verification checklist (operator step, not part of CI)**

Document this in the final commit message for the implementor's reference. Not automated.

1. Re-auth one Google nickname: `darek calendar refresh-token <nickname>`. Confirm a new token file is written under `~/.darek/oauth/<nickname>.json`.
2. Start the agent (`darek chat`) and ask: "create an event in `<nickname>` tomorrow at 3pm called 'plan smoke test'". Verify the agent's tool call succeeds and an event appears in Google Calendar.
3. Ask: "list my events for tomorrow". Confirm the new event is present, copy its UID from the agent's reply.
4. Ask: "rename the 3pm event tomorrow to 'plan smoke test 2'". Verify the title changes in Google Calendar.
5. Ask: "delete that event". Verify it disappears from Google Calendar.
6. Optional: create an event with `attendees=["someone@example.com"]` and `send_invites=true`; confirm Google sends an invite email.
7. Try targeting an iCal calendar with `create_event`; confirm the error message says `read-only`.

- [ ] **Step 4: No code commit for verification (manual). Plan complete.**
