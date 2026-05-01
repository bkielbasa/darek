package digest

import (
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
