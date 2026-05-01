# Darek — calendar write tools (design)

**Date:** 2026-05-01
**Status:** approved (awaiting implementation plan)
**Author:** brainstormed with Claude

## 1. Goal

Let the agent create, update, and delete calendar events on configured Google calendars. Today the agent only has `calendar.list_events`; this spec adds the write surface so requests like "schedule lunch with Bart tomorrow at 1pm" or "move my 3pm to 4pm" execute end-to-end.

## 2. Scope

### In

- Three new agent tools: `calendar.create_event`, `calendar.update_event`, `calendar.delete_event`.
- Google Calendar backend implements writes via the existing `google.golang.org/api/calendar/v3` client.
- OAuth scope widened from `CalendarReadonlyScope` to `CalendarEventsScope` (read + create/update/delete events; cannot manage calendars themselves).
- Optional attendees on create/update; optional `send_invites` flag (default `false`) that maps to Google's `sendUpdates`.
- PATCH-style updates: only fields present in the call are changed.
- Operations on recurring events act on the single instance whose ID was returned by `list_events` (which already uses `SingleEvents(true)`).
- Type-level separation between read-only and writable sources via a new `WritableCalendarSource` interface.

### Out (deferred)

- ICS write paths. The current ICS source is a URL-fed read-only feed — there is no meaningful write target.
- Series-level recurring edits ("delete this recurring meeting forever"). Out of scope v1.
- Recurrence rules (`RRULE`) on create/update. Agent can create individual events; recurring patterns added later.
- Reminders/notifications config beyond `sendUpdates`.
- Conflict detection / dedup. The agent can `list_events` first if it wants to avoid duplicates.
- Live integration test against real Google Calendar (no read-side integration test exists either; adding one needs a dedicated test calendar and is out of this scope).
- Calendar management (create/delete entire calendars). `CalendarEventsScope` doesn't allow this and we don't want it.

## 3. Architecture

```
tools/calendar/
  calendar.go         existing Event, CalendarSource, Sources
                      + WritableCalendarSource interface
                      + ErrReadOnly sentinel
                      + Sources.Create / Sources.Update / Sources.Delete
                        dispatch helpers (look up nickname, type-assert,
                        return ErrReadOnly otherwise)
  tool.go             existing ListEventsTool
                      + CreateEventTool, UpdateEventTool, DeleteEventTool
  google/google.go    scope const → CalendarEventsScope
                      + (*Source).CreateEvent
                      + (*Source).UpdateEvent
                      + (*Source).DeleteEvent
                      Conversion helpers from NewEvent / EventPatch into the
                      Google API types, mirroring the existing convert().
  google/oauth.go     unchanged. Existing tokens become invalid for the new
                      scope and trigger re-consent on next use.
```

### 3.1 Interfaces and types

```go
// tools/calendar/calendar.go
var ErrReadOnly = errors.New("calendar is read-only")

type WritableCalendarSource interface {
    CalendarSource
    CreateEvent(ctx context.Context, in NewEvent) (Event, error)
    UpdateEvent(ctx context.Context, uid string, patch EventPatch) (Event, error)
    DeleteEvent(ctx context.Context, uid string, sendInvites bool) error
}

type NewEvent struct {
    Summary, Description, Location string
    Start, End                     time.Time
    AllDay                         bool
    Attendees                      []string // emails
    SendInvites                    bool
}

// Pointer fields = "set if non-nil, leave alone if nil" → PATCH semantics.
// Attendees uses *[]string so nil = no change, &[]string{} = clear all.
type EventPatch struct {
    Summary, Description, Location *string
    Start, End                     *time.Time
    AllDay                         *bool
    Attendees                      *[]string
    SendInvites                    bool
}
```

`Sources` gets thin dispatch helpers:

```go
func (s *Sources) Create(ctx context.Context, calendar string, in NewEvent) (Event, error)
func (s *Sources) Update(ctx context.Context, calendar, uid string, patch EventPatch) (Event, error)
func (s *Sources) Delete(ctx context.Context, calendar, uid string, sendInvites bool) error
```

Each looks up the nickname, type-asserts to `WritableCalendarSource`, and returns `fmt.Errorf("calendar %q: %w", calendar, ErrReadOnly)` if the assertion fails. Mirrors the existing `ListEvents` error-shape patterns.

### 3.2 Tool layer presence detection

`update_event` needs PATCH semantics — a missing JSON key must mean "don't touch", not "clear". The tool decodes args in two passes:

1. `map[string]json.RawMessage` to detect which keys are present.
2. For each present key, decode into the corresponding field of `EventPatch`.

This handles `attendees: []` (clear all) vs `attendees` absent (no change) cleanly, which a single typed struct with `omitempty` cannot.

`create_event` uses a regular typed struct — no PATCH semantics there.

## 4. Tool surfaces

All tools follow the existing `tools.Tool` interface pattern. Output strings keep the format `[calendar] start — summary` that `list_events` already uses, so the agent can reason consistently across reads and writes.

### 4.1 `calendar.create_event`

```json
{
  "type": "object",
  "properties": {
    "calendar":     {"type": "string", "description": "nickname of a writable calendar"},
    "summary":      {"type": "string"},
    "start":        {"type": "string", "description": "RFC3339 datetime, or YYYY-MM-DD if all_day=true"},
    "end":          {"type": "string", "description": "same format; defaults to start+1h (timed) or start+1 day (all_day)"},
    "all_day":      {"type": "boolean", "default": false},
    "description":  {"type": "string"},
    "location":     {"type": "string"},
    "attendees":    {"type": "array", "items": {"type": "string"}},
    "send_invites": {"type": "boolean", "default": false}
  },
  "required": ["calendar", "summary", "start"]
}
```

Returns:
```
[work] 2026-05-02T15:00:00+02:00 — Lunch with Bart @ La Cantine
uid: abc123def456
```

### 4.2 `calendar.update_event`

```json
{
  "type": "object",
  "properties": {
    "calendar":     {"type": "string"},
    "uid":          {"type": "string", "description": "from list_events"},
    "summary":      {"type": "string"},
    "start":        {"type": "string"},
    "end":          {"type": "string"},
    "all_day":      {"type": "boolean"},
    "description":  {"type": "string"},
    "location":     {"type": "string"},
    "attendees":    {"type": "array", "items": {"type": "string"}, "description": "replaces full attendee list"},
    "send_invites": {"type": "boolean", "default": false}
  },
  "required": ["calendar", "uid"]
}
```

PATCH semantics enforced via the two-pass decode in §3.2. If no patch fields are present, the tool returns `"no fields to update"` rather than firing an empty PATCH.

Returns the updated event in the same format as `create_event`.

### 4.3 `calendar.delete_event`

```json
{
  "type": "object",
  "properties": {
    "calendar":     {"type": "string"},
    "uid":          {"type": "string"},
    "send_invites": {"type": "boolean", "default": false}
  },
  "required": ["calendar", "uid"]
}
```

Returns: `"deleted: <uid> from <calendar>"`.

## 5. Defaults and conventions

- **Default duration** (create only): 1 hour for timed events, 1 day for all-day. Matches Google's UI default. Applied by the tool layer before calling the source. On update, an absent `end` means "no change" (PATCH); defaults are not re-applied.
- **Time zones**: agent passes RFC3339 with offset (e.g. `2026-05-02T15:00:00+02:00`), matching the existing read flow. No naive-local + TZID convenience field.
- **All-day events**: `all_day=true` requires `start` (and `end` if present) to be `YYYY-MM-DD`. End is exclusive (Google's convention).
- **No dedup on create**. The agent calls `list_events` first if it wants to avoid duplicates.
- **`send_invites`** maps to Google's `sendUpdates`: `true` → `"all"`, `false` → `"none"`. Default `false` so speculative tool calls don't email people.
- **Recurring events**: instance-only. Agent passes the instance UID returned by `list_events` (already expanded via `SingleEvents(true)`); modifications affect that single instance.

## 6. OAuth scope migration

Google sources currently use `calendar.CalendarReadonlyScope`. The constant in `google.go` widens to `calsvc.CalendarEventsScope` (read + write events; cannot manage calendars).

Existing OAuth tokens were issued for the read-only scope. After this change:

- `s.cfg.Client(ctx, tok)` will still produce an HTTP client, and Google may even accept reads with the old token, but writes will fail with an authorization error.
- Operator must re-run the existing CLI auth flow (`darek` calendar auth subcommand) once per nickname to mint a new token covering `CalendarEventsScope`.
- Spec calls this out as a one-time operator step. The tool does not auto-recover at runtime.

Implementation plan should include a release note / README update calling this out.

## 7. Error handling

- **Unknown calendar nickname**: existing `unknown calendar %q (have: ...)` error from `Sources`.
- **Read-only calendar**: `fmt.Errorf("calendar %q: %w", name, ErrReadOnly)` from `Sources.Create/Update/Delete`. Tool surfaces as `calendar "home-ics" is read-only`.
- **Stale OAuth token**: surfaced as the underlying Google API error (`googleapi.Error` with 401/403). Operator-fixable, not a tool concern.
- **Validation in the tool layer** (before any source call):
  - empty `summary` on create
  - `end` < `start` (after defaults applied on create; on update, only enforced when both `start` and `end` are present in the patch — partial start-only or end-only edits pass through)
  - malformed RFC3339 / `YYYY-MM-DD`
  - `all_day=true` with a datetime instead of a date (on create, or on update for whichever of `start`/`end`/`all_day` is being patched)
  - `update_event` with no patch fields
- **Google API errors**: wrapped as `events.insert: %w` / `events.patch: %w` / `events.delete: %w`, mirroring the existing `events.list: %w`. Each call wrapped in `obs.Dep(ctx, "google_calendar", "<op>", ...)` so spans and dependency metrics record the call.

## 8. Testing

Mirrors the existing `tools/calendar/calendar_test.go`, `tool_test.go`, `google/oauth_test.go`, `ical/ical_test.go` style.

### `tools/calendar/tool_test.go` additions

Table-driven unit tests using a fake `WritableCalendarSource` that records calls. Cases:

- Happy path for each tool: source called with the expected `NewEvent` / `EventPatch` / `(uid, sendInvites)`.
- Read-only calendar: tool returns error message containing the nickname and `read-only`.
- Unknown calendar nickname: existing error format.
- Validation errors: empty summary, `end < start`, malformed RFC3339, `update_event` with no patch fields, `all_day=true` with datetime input.
- PATCH presence: `update_event` with `attendees: []` clears, with `attendees` absent leaves alone.
- `send_invites` flag round-trips into `NewEvent.SendInvites` / `EventPatch.SendInvites` / delete arg.

### `tools/calendar/google/google_test.go` (new)

Tests the conversion layer. No live API call.

- `NewEvent` → `*calsvc.Event` for both timed and all-day inputs (verifies `DateTime` vs `Date` field placement and end-exclusive day handling).
- `EventPatch` → patch payload includes only present fields (pointer-nil fields omitted).
- `*calsvc.Event` returned from create/patch → `calendar.Event` round-trip via existing `convert()`.

### Manual verification checklist (in the implementation plan)

- Re-auth one Google nickname with the widened scope.
- `create_event` → `list_events` → confirm event appears.
- `update_event` (change summary only) → `list_events` → confirm only summary changed.
- `delete_event` → `list_events` → confirm gone.
- `create_event` with `attendees=[someone@example.com]`, `send_invites=true` → confirm Google emails the invitee.

## 9. Open questions

None — all decisions resolved during brainstorming. Spec is ready for an implementation plan.
