package digest

import (
	"strings"
	"testing"
	"time"

	"darek/tools/calendar"

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
			End:   time.Date(2026, 5, 3, 0, 0, 0, 0, loc),
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
			End:   time.Date(2026, 5, 5, 17, 0, 0, 0, loc),
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
	require.Equal(t, 1, strings.Count(html, "Today"))
	require.Contains(t, html, "Friday")
	require.Contains(t, html, "Saturday")
	require.Contains(t, html, "Sunday")
	require.Contains(t, html, "Nothing scheduled")
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

func TestBuildEmail_HasRequiredHeadersAndBothParts(t *testing.T) {
	out, err := BuildEmail(EmailInput{
		From:     "me@example.com",
		To:       "you@example.com",
		Subject:  "Calendar — 2026-05-01",
		Text:     "plain body",
		HTML:     "<html><body>html body</body></html>",
		Date:     time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC),
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
