# Calendar Daily Digest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `darek calendar daily-digest` subcommand that emails a 3-day calendar digest (today + 2 more days) from all configured calendars, rendered as multipart/alternative (HTML + plaintext), sent via an existing configured mail account.

**Architecture:** A new `tools/calendar/digest` package contains all pure logic — window computation, day bucketing (with multi-day event expansion), text + HTML rendering, and MIME multipart/alternative envelope construction. `cmd/darek/daily_digest.go` glues config, calendar sources, and SMTP. The subcommand registers under existing `darek calendar <subcmd>` dispatch.

**Tech Stack:** Go stdlib (`html/template`, `text/template`, `mime/multipart`, `net/textproto`, `crypto/rand`), existing `tools/mail/smtp.Sender`, existing `tools/calendar.Sources`.

**Design source:** brainstormed in conversation 2026-05-01; user explicitly accepted a 5-section design (architecture, config, content/rendering with pretty HTML, error handling, testing).

**Out of scope:** LLM-augmented summaries, in-server scheduler, per-calendar recipient routing, retries (cron's job).

---

## File Map

| Path | Responsibility |
|---|---|
| `config/types.go` | (modify) add `CalendarDigest` struct + field on `Config`. |
| `tools/calendar/digest/digest.go` | (create) `Window`, `DayBucket`, `Group`, `RenderText`, `RenderHTML`, `BuildEmail`, plus the embedded HTML template. |
| `tools/calendar/digest/digest_test.go` | (create) unit tests for all of the above. |
| `cmd/darek/daily_digest.go` | (create) `runDailyDigest(ctx, cfgPath)` — config → sources → window → render → SMTP send. |
| `cmd/darek/refresh_token.go` | (modify) add `daily-digest` case to `runCalendar`. |

---

## Task 1 — Config: `CalendarDigest` block

**Files:**
- Modify: `config/types.go`

- [ ] **Step 1: Add the struct and field**

In `config/types.go`, add this struct at an appropriate position (e.g. after `Mail`):

```go
type CalendarDigest struct {
	To          string `yaml:"to"`
	FromAccount string `yaml:"from_account"`
	Subject     string `yaml:"subject"` // optional; default "Calendar — <YYYY-MM-DD>"
}
```

In the `Config` struct, add a field:

```go
CalendarDigest CalendarDigest `yaml:"calendar_digest"`
```

- [ ] **Step 2: Build to verify**

Run: `cd /Users/bklimczak/Projects/darek && go build ./config/...`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add config/types.go
git commit -m "feat(config): add CalendarDigest block (to, from_account, subject)"
```

---

## Task 2 — `digest.Window` (TDD)

Computes the inclusive 3-calendar-day window starting at today's local-midnight.

**Files:**
- Create: `tools/calendar/digest/digest.go`
- Create: `tools/calendar/digest/digest_test.go`

- [ ] **Step 1: Create test file with failing test**

Create `tools/calendar/digest/digest_test.go`:

```go
package digest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWindow_UTC(t *testing.T) {
	now := time.Date(2026, 5, 1, 8, 30, 0, 0, time.UTC)
	from, to := Window(now)
	require.Equal(t, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), from)
	require.Equal(t, time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC), to)
}

func TestWindow_OffsetTZ(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 5, 1, 23, 30, 0, 0, loc)
	from, to := Window(now)
	require.Equal(t, time.Date(2026, 5, 1, 0, 0, 0, 0, loc), from)
	require.Equal(t, time.Date(2026, 5, 4, 0, 0, 0, 0, loc), to)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/calendar/digest/ -v`
Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Implement `Window`**

Create `tools/calendar/digest/digest.go`:

```go
// Package digest renders a 3-day calendar digest into plaintext + HTML.
package digest

import "time"

// Window returns the [from, to) bounds of the digest window: today's local
// midnight through the start of the day after tomorrow + 1 (i.e. 3 calendar
// days starting at the local midnight of `now`). The TZ of the returned
// times is the TZ of `now`.
func Window(now time.Time) (from, to time.Time) {
	from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to = from.AddDate(0, 0, 3)
	return from, to
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/calendar/digest/ -v`
Expected: PASS for both `TestWindow_UTC` and `TestWindow_OffsetTZ`.

- [ ] **Step 5: Commit**

```bash
git add tools/calendar/digest/digest.go tools/calendar/digest/digest_test.go
git commit -m "feat(calendar/digest): add Window for 3-day local-midnight bounds"
```

---

## Task 3 — `digest.DayBucket` + `digest.Group` (TDD)

Buckets events into one entry per calendar day in the window, expanding multi-day events into every day they overlap. Within a day, all-day events sort before timed events; timed events sort by start ascending.

**Files:**
- Modify: `tools/calendar/digest/digest.go`
- Modify: `tools/calendar/digest/digest_test.go`

- [ ] **Step 1: Append failing tests**

Append to `tools/calendar/digest/digest_test.go`:

```go
import (
	"darek/tools/calendar"
)

// (Adjust the existing import block to include "darek/tools/calendar" if it isn't already present.)

func TestGroup_TimedEventInDay1(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 5, 1, 8, 0, 0, 0, loc)
	from, to := Window(now)
	events := []calendar.Event{
		{Calendar: "work", Summary: "Standup", Start: time.Date(2026, 5, 1, 9, 0, 0, 0, loc), End: time.Date(2026, 5, 1, 10, 0, 0, 0, loc)},
	}
	buckets := Group(events, from, to)
	require.Len(t, buckets, 3)
	require.Equal(t, time.Date(2026, 5, 1, 0, 0, 0, 0, loc), buckets[0].Date)
	require.Len(t, buckets[0].Events, 1)
	require.Equal(t, "Standup", buckets[0].Events[0].Summary)
	require.Empty(t, buckets[1].Events)
	require.Empty(t, buckets[2].Events)
}

func TestGroup_AllDayEventSpansTwoDays(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 5, 1, 8, 0, 0, 0, loc)
	from, to := Window(now)
	events := []calendar.Event{
		{
			Calendar: "personal", Summary: "Vacation", AllDay: true,
			Start: time.Date(2026, 5, 1, 0, 0, 0, 0, loc),
			End:   time.Date(2026, 5, 3, 0, 0, 0, 0, loc), // exclusive end (Google convention)
		},
	}
	buckets := Group(events, from, to)
	require.Len(t, buckets[0].Events, 1)
	require.Len(t, buckets[1].Events, 1)
	require.Empty(t, buckets[2].Events)
}

func TestGroup_EventStartingBeforeWindowEndsInsideDay1(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 5, 1, 8, 0, 0, 0, loc)
	from, to := Window(now)
	events := []calendar.Event{
		{
			Calendar: "work", Summary: "OvernightOps",
			Start: time.Date(2026, 4, 30, 22, 0, 0, 0, loc),
			End:   time.Date(2026, 5, 1, 6, 0, 0, 0, loc),
		},
	}
	buckets := Group(events, from, to)
	require.Len(t, buckets[0].Events, 1)
	require.Empty(t, buckets[1].Events)
}

func TestGroup_EventStartingInWindowEndsAfter(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 5, 1, 8, 0, 0, 0, loc)
	from, to := Window(now)
	events := []calendar.Event{
		{
			Calendar: "work", Summary: "Conf",
			Start: time.Date(2026, 5, 3, 10, 0, 0, 0, loc),
			End:   time.Date(2026, 5, 5, 17, 0, 0, 0, loc), // ends after window
		},
	}
	buckets := Group(events, from, to)
	require.Empty(t, buckets[0].Events)
	require.Empty(t, buckets[1].Events)
	require.Len(t, buckets[2].Events, 1)
}

func TestGroup_NoEvents(t *testing.T) {
	loc := time.UTC
	from, to := Window(time.Date(2026, 5, 1, 8, 0, 0, 0, loc))
	buckets := Group(nil, from, to)
	require.Len(t, buckets, 3)
	for i, b := range buckets {
		require.Empty(t, b.Events, "bucket %d should be empty", i)
	}
}

func TestGroup_SortAllDayBeforeTimedThenByStart(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 5, 1, 8, 0, 0, 0, loc)
	from, to := Window(now)
	events := []calendar.Event{
		{Calendar: "a", Summary: "T-late", Start: time.Date(2026, 5, 1, 14, 0, 0, 0, loc), End: time.Date(2026, 5, 1, 15, 0, 0, 0, loc)},
		{Calendar: "a", Summary: "T-early", Start: time.Date(2026, 5, 1, 9, 0, 0, 0, loc), End: time.Date(2026, 5, 1, 10, 0, 0, 0, loc)},
		{Calendar: "b", Summary: "All-day", AllDay: true, Start: time.Date(2026, 5, 1, 0, 0, 0, 0, loc), End: time.Date(2026, 5, 2, 0, 0, 0, 0, loc)},
	}
	buckets := Group(events, from, to)
	require.Equal(t, "All-day", buckets[0].Events[0].Summary)
	require.Equal(t, "T-early", buckets[0].Events[1].Summary)
	require.Equal(t, "T-late", buckets[0].Events[2].Summary)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/calendar/digest/ -v`
Expected: FAIL — `Group`, `DayBucket` undefined.

- [ ] **Step 3: Implement `DayBucket` and `Group`**

Append to `tools/calendar/digest/digest.go`:

```go
import (
	"sort"
	"time"

	"darek/tools/calendar"
)

// (If digest.go currently has `import "time"`, replace it with the block above.)

// DayBucket holds the events that overlap a single calendar day.
type DayBucket struct {
	Date   time.Time         // local midnight of the day
	Events []calendar.Event  // sorted: all-day first, then timed by Start
}

// Group buckets events into one DayBucket per calendar day in [from, to).
// An event appears in every day bucket it overlaps. Events spanning multiple
// days are repeated across each overlapping day. Within a day the order is:
// all-day events first, then timed events ascending by Start.
//
// `from` must be local midnight; `to` is `from + 3 days`.
func Group(events []calendar.Event, from, to time.Time) []DayBucket {
	const days = 3
	buckets := make([]DayBucket, days)
	for i := 0; i < days; i++ {
		buckets[i] = DayBucket{Date: from.AddDate(0, 0, i)}
	}
	for _, ev := range events {
		// Determine each day's [start, end) and check overlap with the event.
		for i := 0; i < days; i++ {
			dayStart := buckets[i].Date
			dayEnd := dayStart.AddDate(0, 0, 1)
			if overlaps(ev.Start, ev.End, dayStart, dayEnd) {
				buckets[i].Events = append(buckets[i].Events, ev)
			}
		}
	}
	for i := range buckets {
		sortBucket(buckets[i].Events)
	}
	return buckets
}

func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	// Half-open intervals [start, end). End-equal-to-start is non-overlapping.
	return aStart.Before(bEnd) && aEnd.After(bStart)
}

func sortBucket(evs []calendar.Event) {
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].AllDay != evs[j].AllDay {
			return evs[i].AllDay // all-day comes first
		}
		return evs[i].Start.Before(evs[j].Start)
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/calendar/digest/ -v`
Expected: all `TestGroup_*` tests PASS, plus existing `TestWindow_*`.

- [ ] **Step 5: Commit**

```bash
git add tools/calendar/digest/digest.go tools/calendar/digest/digest_test.go
git commit -m "feat(calendar/digest): add Group with multi-day expansion and ordering"
```

---

## Task 4 — `digest.RenderText` (TDD)

Plaintext renderer. Golden-string assertion on a known input.

**Files:**
- Modify: `tools/calendar/digest/digest.go`
- Modify: `tools/calendar/digest/digest_test.go`

- [ ] **Step 1: Append failing test**

Append to `tools/calendar/digest/digest_test.go`:

```go
func TestRenderText_GoldenSample(t *testing.T) {
	loc := time.UTC
	d0 := time.Date(2026, 5, 1, 0, 0, 0, 0, loc)
	d1 := d0.AddDate(0, 0, 1)
	d2 := d0.AddDate(0, 0, 2)
	buckets := []DayBucket{
		{Date: d0, Events: []calendar.Event{
			{Calendar: "personal", Summary: "Vacation", AllDay: true, Start: d0, End: d1},
			{Calendar: "work", Summary: "Standup", Start: time.Date(2026, 5, 1, 9, 0, 0, 0, loc), End: time.Date(2026, 5, 1, 9, 30, 0, 0, loc)},
			{Calendar: "personal", Summary: "Lunch with Bart", Location: "La Cantine", Start: time.Date(2026, 5, 1, 12, 30, 0, 0, loc), End: time.Date(2026, 5, 1, 13, 30, 0, 0, loc)},
		}},
		{Date: d1, Events: nil},
		{Date: d2, Events: []calendar.Event{
			{Calendar: "work", Summary: "Quarterly planning", Start: time.Date(2026, 5, 3, 10, 0, 0, 0, loc), End: time.Date(2026, 5, 3, 11, 0, 0, 0, loc)},
		}},
	}
	want := "" +
		"Friday 2026-05-01\n" +
		"  (all day) [personal] Vacation\n" +
		"  09:00–09:30 [work] Standup\n" +
		"  12:30–13:30 [personal] Lunch with Bart @ La Cantine\n" +
		"\n" +
		"Saturday 2026-05-02\n" +
		"  Nothing scheduled\n" +
		"\n" +
		"Sunday 2026-05-03\n" +
		"  10:00–11:00 [work] Quarterly planning\n"
	got := RenderText(buckets)
	require.Equal(t, want, got)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/calendar/digest/ -run TestRenderText_GoldenSample -v`
Expected: FAIL — `RenderText` undefined.

- [ ] **Step 3: Implement `RenderText`**

Append to `tools/calendar/digest/digest.go`:

```go
import (
	"fmt"
	"strings"
)

// (Merge with the existing import block.)

// RenderText returns the plaintext digest body. Day blocks are separated by
// a blank line. Each event line is indented two spaces.
func RenderText(buckets []DayBucket) string {
	var b strings.Builder
	for i, bk := range buckets {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s %s\n", bk.Date.Format("Monday"), bk.Date.Format("2006-01-02"))
		if len(bk.Events) == 0 {
			b.WriteString("  Nothing scheduled\n")
			continue
		}
		for _, ev := range bk.Events {
			b.WriteString("  ")
			if ev.AllDay {
				b.WriteString("(all day)")
			} else {
				fmt.Fprintf(&b, "%s–%s", ev.Start.Format("15:04"), ev.End.Format("15:04"))
			}
			fmt.Fprintf(&b, " [%s] %s", ev.Calendar, ev.Summary)
			if ev.Location != "" {
				fmt.Fprintf(&b, " @ %s", ev.Location)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/calendar/digest/ -v`
Expected: all tests pass including the new `TestRenderText_GoldenSample`.

- [ ] **Step 5: Commit**

```bash
git add tools/calendar/digest/digest.go tools/calendar/digest/digest_test.go
git commit -m "feat(calendar/digest): add RenderText with empty-day handling"
```

---

## Task 5 — `digest.RenderHTML` (TDD)

Pretty HTML output. Card per day, calendar nickname as a colored pill (deterministic by hash), today badge, system font stack, table-based for client compatibility, all values escaped via `html/template`.

**Files:**
- Modify: `tools/calendar/digest/digest.go`
- Modify: `tools/calendar/digest/digest_test.go`

- [ ] **Step 1: Append failing tests**

Append to `tools/calendar/digest/digest_test.go`:

```go
func TestRenderHTML_EscapesHostileTitles(t *testing.T) {
	loc := time.UTC
	d0 := time.Date(2026, 5, 1, 0, 0, 0, 0, loc)
	buckets := []DayBucket{
		{Date: d0, Events: []calendar.Event{
			{Calendar: "work", Summary: "<script>alert(1)</script>", Start: time.Date(2026, 5, 1, 9, 0, 0, 0, loc), End: time.Date(2026, 5, 1, 10, 0, 0, 0, loc)},
		}},
		{Date: d0.AddDate(0, 0, 1), Events: nil},
		{Date: d0.AddDate(0, 0, 2), Events: nil},
	}
	html := RenderHTML(buckets, d0)
	require.NotContains(t, html, "<script>alert(1)</script>")
	require.Contains(t, html, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

func TestRenderHTML_StructuralElements(t *testing.T) {
	loc := time.UTC
	d0 := time.Date(2026, 5, 1, 0, 0, 0, 0, loc)
	buckets := []DayBucket{
		{Date: d0, Events: []calendar.Event{
			{Calendar: "work", Summary: "Standup", Start: time.Date(2026, 5, 1, 9, 0, 0, 0, loc), End: time.Date(2026, 5, 1, 10, 0, 0, 0, loc)},
		}},
		{Date: d0.AddDate(0, 0, 1), Events: nil},
		{Date: d0.AddDate(0, 0, 2), Events: nil},
	}
	html := RenderHTML(buckets, d0)
	// Today badge appears on the first card only.
	require.Equal(t, 1, strings.Count(html, "Today"))
	// One day header per bucket (Friday/Saturday/Sunday).
	require.Contains(t, html, "Friday")
	require.Contains(t, html, "Saturday")
	require.Contains(t, html, "Sunday")
	// Empty days carry the "Nothing scheduled" copy.
	require.Contains(t, html, "Nothing scheduled")
	// Calendar nickname appears.
	require.Contains(t, html, "work")
}

func TestRenderHTML_NicknameColorIsDeterministic(t *testing.T) {
	c1 := pillColor("work")
	c2 := pillColor("work")
	c3 := pillColor("personal")
	require.Equal(t, c1, c2)
	require.NotEqual(t, c1, c3)
	require.Regexp(t, `^#[0-9a-fA-F]{6}$`, c1)
}
```

Add `"strings"` to the test file's import block if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/calendar/digest/ -run TestRenderHTML -v`
Expected: FAIL — `RenderHTML`, `pillColor` undefined.

- [ ] **Step 3: Implement `RenderHTML` and `pillColor`**

Append to `tools/calendar/digest/digest.go`:

```go
import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"html/template"
)

// (Merge with the existing import block.)

// pillColor returns a deterministic background color for a calendar nickname
// pill, derived from the first 3 bytes of SHA-256(nickname). The color sits
// in the lightness band suitable for a small pill on a white background.
func pillColor(nickname string) string {
	sum := sha256.Sum256([]byte(nickname))
	// Mix into a pastel: clamp each channel to [0xb0, 0xff].
	clamp := func(b byte) byte { return 0xb0 + (b % 0x50) }
	return fmt.Sprintf("#%02x%02x%02x", clamp(sum[0]), clamp(sum[1]), clamp(sum[2]))
}

type htmlEvent struct {
	IsAllDay   bool
	TimeRange  string // "09:00–10:00" or "all day"
	Calendar   string
	PillColor  string
	Summary    string
	Location   string
}

type htmlDay struct {
	Weekday  string
	ISODate  string // "2026-05-01"
	Pretty   string // "May 1, 2026"
	IsToday  bool
	Events   []htmlEvent
}

const htmlTemplateSrc = `<!DOCTYPE html>
<html><body style="margin:0;padding:24px;background:#f5f5f7;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;color:#1d1d1f;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;margin:0 auto;width:100%;">
{{range .Days}}
  <tr><td style="padding:0 0 16px 0;">
    <table role="presentation" cellpadding="0" cellspacing="0" border="0" style="width:100%;background:#ffffff;border:1px solid #e5e5ea;border-radius:8px;">
      <tr><td style="padding:18px 20px 12px 20px;">
        <div style="font-size:18px;font-weight:600;line-height:1.2;">
          {{.Weekday}}
          {{if .IsToday}}<span style="display:inline-block;margin-left:8px;padding:2px 8px;background:#0071e3;color:#ffffff;font-size:11px;font-weight:600;border-radius:10px;vertical-align:middle;">Today</span>{{end}}
          <span style="font-weight:400;color:#86868b;font-size:14px;margin-left:6px;">{{.Pretty}}</span>
        </div>
      </td></tr>
      {{if .Events}}
        <tr><td style="padding:0 20px 16px 20px;">
          <table role="presentation" cellpadding="0" cellspacing="0" border="0" style="width:100%;border-collapse:collapse;">
            {{range .Events}}
            <tr>
              <td style="padding:8px 12px 8px 0;font-family:ui-monospace,'SFMono-Regular',Menlo,monospace;color:#6e6e73;font-size:13px;white-space:nowrap;vertical-align:top;width:1%;">{{.TimeRange}}</td>
              <td style="padding:8px 12px 8px 0;vertical-align:top;width:1%;">
                <span style="display:inline-block;padding:2px 8px;background:{{.PillColor}};color:#1d1d1f;font-size:11px;font-weight:600;border-radius:10px;white-space:nowrap;">{{.Calendar}}</span>
              </td>
              <td style="padding:8px 0;vertical-align:top;">
                <div style="font-size:14px;font-weight:500;line-height:1.3;">{{.Summary}}</div>
                {{if .Location}}<div style="font-size:12px;color:#86868b;margin-top:2px;">{{.Location}}</div>{{end}}
              </td>
            </tr>
            {{end}}
          </table>
        </td></tr>
      {{else}}
        <tr><td style="padding:0 20px 20px 20px;text-align:center;color:#86868b;font-size:13px;">Nothing scheduled</td></tr>
      {{end}}
    </table>
  </td></tr>
{{end}}
</table>
</body></html>`

var htmlTemplate = template.Must(template.New("digest").Parse(htmlTemplateSrc))

// RenderHTML returns the HTML digest body. `today` (in the same TZ as the
// buckets) determines which card gets the "Today" badge.
func RenderHTML(buckets []DayBucket, today time.Time) string {
	todayMidnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
	days := make([]htmlDay, 0, len(buckets))
	for _, bk := range buckets {
		d := htmlDay{
			Weekday: bk.Date.Format("Monday"),
			ISODate: bk.Date.Format("2006-01-02"),
			Pretty:  bk.Date.Format("January 2, 2006"),
			IsToday: bk.Date.Equal(todayMidnight),
		}
		for _, ev := range bk.Events {
			tr := "all day"
			if !ev.AllDay {
				tr = ev.Start.Format("15:04") + "–" + ev.End.Format("15:04")
			}
			d.Events = append(d.Events, htmlEvent{
				IsAllDay:  ev.AllDay,
				TimeRange: tr,
				Calendar:  ev.Calendar,
				PillColor: pillColor(ev.Calendar),
				Summary:   ev.Summary,
				Location:  ev.Location,
			})
		}
		days = append(days, d)
	}
	var buf bytes.Buffer
	if err := htmlTemplate.Execute(&buf, struct{ Days []htmlDay }{days}); err != nil {
		// template errors are programming bugs, not runtime conditions
		return "template error: " + err.Error()
	}
	return buf.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/calendar/digest/ -v`
Expected: all tests pass including the three new `TestRenderHTML_*`.

- [ ] **Step 5: Commit**

```bash
git add tools/calendar/digest/digest.go tools/calendar/digest/digest_test.go
git commit -m "feat(calendar/digest): add RenderHTML with pretty card layout"
```

---

## Task 6 — `digest.BuildEmail` (TDD)

Constructs an RFC 5322 multipart/alternative envelope from text + HTML bodies. Pure function — takes ready-made bodies, returns bytes ready for SMTP.

**Files:**
- Modify: `tools/calendar/digest/digest.go`
- Modify: `tools/calendar/digest/digest_test.go`

- [ ] **Step 1: Append failing tests**

Append to `tools/calendar/digest/digest_test.go`:

```go
func TestBuildEmail_HasRequiredHeadersAndBothParts(t *testing.T) {
	out, err := BuildEmail(EmailInput{
		From:    "me@example.com",
		To:      "you@example.com",
		Subject: "Calendar — 2026-05-01",
		Text:    "plain body",
		HTML:    "<html><body>html body</body></html>",
		Date:    time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC),
		Hostname: "example.com",
	})
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, "From: me@example.com")
	require.Contains(t, s, "To: you@example.com")
	require.Contains(t, s, "Subject: Calendar")
	require.Contains(t, s, "MIME-Version: 1.0")
	require.Contains(t, s, "Content-Type: multipart/alternative")
	require.Contains(t, s, "plain body")
	require.Contains(t, s, "<html><body>html body</body></html>")
	require.Contains(t, s, "Message-ID: <")
}

func TestBuildEmail_RequiresFromAndTo(t *testing.T) {
	_, err := BuildEmail(EmailInput{To: "you@example.com", Text: "x", HTML: "x"})
	require.Error(t, err)
	_, err = BuildEmail(EmailInput{From: "me@example.com", Text: "x", HTML: "x"})
	require.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/calendar/digest/ -run TestBuildEmail -v`
Expected: FAIL — `BuildEmail` undefined.

- [ ] **Step 3: Implement `BuildEmail`**

Append to `tools/calendar/digest/digest.go`:

```go
import (
	"crypto/rand"
	"encoding/hex"
	"mime"
	"mime/multipart"
	"net/textproto"
)

// (Merge with the existing import block.)

type EmailInput struct {
	From     string
	To       string
	Subject  string
	Text     string
	HTML     string
	Date     time.Time // optional; defaults to time.Now()
	Hostname string    // for Message-ID; defaults to "darek.local"
}

// BuildEmail returns RFC 5322 bytes ready to hand to an SMTP sender.
func BuildEmail(in EmailInput) ([]byte, error) {
	if in.From == "" {
		return nil, fmt.Errorf("from required")
	}
	if in.To == "" {
		return nil, fmt.Errorf("to required")
	}
	if in.Date.IsZero() {
		in.Date = time.Now()
	}
	host := in.Hostname
	if host == "" {
		host = "darek.local"
	}
	mid, err := generateMessageID(host)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	hdr := textproto.MIMEHeader{}
	hdr.Set("From", in.From)
	hdr.Set("To", in.To)
	hdr.Set("Subject", encodeSubject(in.Subject))
	hdr.Set("Date", in.Date.Format(time.RFC1123Z))
	hdr.Set("Message-ID", "<"+mid+">")
	hdr.Set("MIME-Version", "1.0")
	hdr.Set("Content-Type", `multipart/alternative; boundary="`+mw.Boundary()+`"`)

	for k, vs := range hdr {
		for _, v := range vs {
			fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
		}
	}
	buf.WriteString("\r\n")

	textPart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {`text/plain; charset="utf-8"`},
		"Content-Transfer-Encoding": {"8bit"},
	})
	if err != nil {
		return nil, fmt.Errorf("create text part: %w", err)
	}
	if _, err := textPart.Write([]byte(in.Text)); err != nil {
		return nil, err
	}

	htmlPart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {`text/html; charset="utf-8"`},
		"Content-Transfer-Encoding": {"8bit"},
	})
	if err != nil {
		return nil, fmt.Errorf("create html part: %w", err)
	}
	if _, err := htmlPart.Write([]byte(in.HTML)); err != nil {
		return nil, err
	}

	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeSubject(s string) string {
	if isASCII(s) {
		return s
	}
	return mime.QEncoding.Encode("utf-8", s)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}

func generateMessageID(host string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return hex.EncodeToString(raw[:]) + "@" + host, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/calendar/digest/ -v`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add tools/calendar/digest/digest.go tools/calendar/digest/digest_test.go
git commit -m "feat(calendar/digest): add BuildEmail multipart/alternative envelope"
```

---

## Task 7 — Subcommand wiring (`darek calendar daily-digest`)

**Files:**
- Create: `cmd/darek/daily_digest.go`
- Modify: `cmd/darek/refresh_token.go` (add dispatch case in `runCalendar`)

This task is glue: config load, calendar source registry, SMTP sender resolution, end-to-end execution. There are no unit tests for the glue (matches the existing `runMailSync` pattern). Manual verification is in Task 8.

- [ ] **Step 1: Create `cmd/darek/daily_digest.go`**

```go
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"darek/config"
	"darek/tools/calendar"
	"darek/tools/calendar/digest"
	googlecal "darek/tools/calendar/google"
	"darek/tools/calendar/ical"
	mailsmtp "darek/tools/mail/smtp"
)

// runDailyDigest sends a 3-day calendar digest email.
// Subcommand: `darek calendar daily-digest`.
func runDailyDigest(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	d := cfg.CalendarDigest
	if d.To == "" {
		return fmt.Errorf("calendar_digest.to is required")
	}
	if d.FromAccount == "" {
		return fmt.Errorf("calendar_digest.from_account is required")
	}
	if len(cfg.Calendars) == 0 {
		return fmt.Errorf("no calendars configured")
	}

	// Find the mail account.
	var mailAcct *config.MailAccountCfg
	for i := range cfg.Mail.Accounts {
		if cfg.Mail.Accounts[i].Nickname == d.FromAccount {
			mailAcct = &cfg.Mail.Accounts[i]
			break
		}
	}
	if mailAcct == nil {
		return fmt.Errorf("calendar_digest.from_account %q not found among mail.accounts", d.FromAccount)
	}
	smtpPassword, err := config.ResolveSecret("env:" + mailAcct.SecretEnv)
	if err != nil {
		return fmt.Errorf("smtp secret for %s: %w", mailAcct.Nickname, err)
	}

	// Build calendar sources (mirrors cmd/darek/chat.go).
	srcs := calendar.NewSources()
	home, _ := os.UserHomeDir()
	tokenStore := googlecal.NewTokenStore(filepath.Join(home, ".darek", "oauth"))
	for _, c := range cfg.Calendars {
		switch c.Kind {
		case "ical":
			if err := srcs.Add(ical.New(c.Nickname, c.URL)); err != nil {
				return fmt.Errorf("calendar %s: %w", c.Nickname, err)
			}
		case "google":
			cid, err := config.ResolveSecret("env:" + c.ClientIDEnv)
			if err != nil {
				return fmt.Errorf("calendar %s client id: %w", c.Nickname, err)
			}
			cs, err := config.ResolveSecret("env:" + c.ClientSecretEnv)
			if err != nil {
				return fmt.Errorf("calendar %s client secret: %w", c.Nickname, err)
			}
			oauthCfg := googlecal.Config(cid, cs)
			if err := srcs.Add(googlecal.NewSource(c.Nickname, c.CalendarID, oauthCfg, tokenStore)); err != nil {
				return fmt.Errorf("calendar %s: %w", c.Nickname, err)
			}
		default:
			return fmt.Errorf("unknown calendar kind %q for nickname %q", c.Kind, c.Nickname)
		}
	}

	// Compute window and fetch events.
	now := time.Now()
	from, to := digest.Window(now)
	events, err := srcs.ListEvents(ctx, from, to, "")
	if err != nil {
		return fmt.Errorf("list events: %w", err)
	}

	// Render.
	buckets := digest.Group(events, from, to)
	text := digest.RenderText(buckets)
	html := digest.RenderHTML(buckets, now)

	// Subject. Optional `{{date}}` token expands to the window start (ISO).
	subject := d.Subject
	if subject == "" {
		subject = "Calendar — " + from.Format("2006-01-02")
	} else {
		subject = strings.ReplaceAll(subject, "{{date}}", from.Format("2006-01-02"))
	}

	// Build envelope.
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "darek.local"
	}
	raw, err := digest.BuildEmail(digest.EmailInput{
		From:     mailAcct.Email,
		To:       d.To,
		Subject:  subject,
		Text:     text,
		HTML:     html,
		Date:     now,
		Hostname: hostname,
	})
	if err != nil {
		return fmt.Errorf("build digest email: %w", err)
	}

	// Send via SMTP.
	sender := mailsmtp.New(mailsmtp.Options{
		Host:     mailAcct.SMTP.Host,
		Port:     mailAcct.SMTP.Port,
		TLS:      mailAcct.SMTP.TLS,
		Username: mailAcct.Username,
		Password: smtpPassword,
	})
	if err := sender.Send(ctx, mailAcct.Email, []string{d.To}, raw); err != nil {
		return fmt.Errorf("send digest: %w", err)
	}
	fmt.Fprintf(os.Stderr, "sent calendar digest to %s (window %s..%s)\n",
		d.To, from.Format("2006-01-02"), to.Format("2006-01-02"))
	return nil
}
```

- [ ] **Step 2: Wire dispatch in `cmd/darek/refresh_token.go`**

Find `runCalendar` (around line 17) and replace its body:

```go
func runCalendar(ctx context.Context, cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: darek calendar <refresh-token|daily-digest> [args...]")
	}
	switch args[0] {
	case "refresh-token":
		if len(args) < 2 {
			return fmt.Errorf("usage: darek calendar refresh-token <nickname>")
		}
		return runRefreshToken(ctx, cfgPath, args[1])
	case "daily-digest":
		return runDailyDigest(ctx, cfgPath)
	default:
		return fmt.Errorf("unknown calendar subcommand %q (try: refresh-token, daily-digest)", args[0])
	}
}
```

- [ ] **Step 3: Build to verify**

Run: `cd /Users/bklimczak/Projects/darek && go build ./cmd/darek`
Expected: clean build.

- [ ] **Step 4: Vet to verify**

Run: `cd /Users/bklimczak/Projects/darek && go vet ./cmd/darek/...`
Expected: clean.

- [ ] **Step 5: Run quick CLI smoke**

Run: `cd /Users/bklimczak/Projects/darek && ./darek 2>&1` (after `make build`) — confirm no panic. Then `./darek calendar` should print the usage error: `usage: darek calendar <refresh-token|daily-digest> [args...]`.

- [ ] **Step 6: Commit**

```bash
git add cmd/darek/daily_digest.go cmd/darek/refresh_token.go
git commit -m "feat(cmd/darek): add 'darek calendar daily-digest' subcommand"
```

---

## Task 8 — Final verification

- [ ] **Step 1: Run all tests**

Run: `cd /Users/bklimczak/Projects/darek && make test`
Expected: all packages pass, including the new `darek/tools/calendar/digest`.

- [ ] **Step 2: Lint**

Run: `cd /Users/bklimczak/Projects/darek && make lint`
Expected: clean.

- [ ] **Step 3: Integration tests**

Run: `cd /Users/bklimczak/Projects/darek && make test-integration`
Expected: pass (no integration tests added in this plan; existing suite must still pass).

- [ ] **Step 4: Manual verification (operator step, not CI)**

Document this in the implementation summary. Not automated.

1. Add to local `darek.yaml`:
   ```yaml
   calendar_digest:
     to: <your-personal-email>
     from_account: <existing-mail-account-nickname>
   ```
2. Run: `darek calendar daily-digest`
3. Confirm an HTML+plaintext digest lands in the recipient inbox.
4. Open the message in Gmail, Apple Mail, and Outlook (or any two of those) — confirm the cards render with rounded corners, calendar pills are colored, and the today badge appears on the first card only.
5. Run `darek calendar daily-digest` again with `calendar_digest.to` removed — confirm a clear stderr error and a non-zero exit code.
