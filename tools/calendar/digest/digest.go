// Package digest renders a 3-day calendar digest into plaintext + HTML.
package digest

import (
	"sort"
	"time"

	"darek/tools/calendar"
)

// Window returns the [from, to) bounds of the digest window: today's local
// midnight through start of the day after tomorrow + 1 (i.e. 3 calendar
// days starting at the local midnight of `now`). The TZ of the returned
// times is the TZ of `now`.
func Window(now time.Time) (from, to time.Time) {
	from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to = from.AddDate(0, 0, 3)
	return from, to
}

// DayBucket holds the events that overlap a single calendar day.
type DayBucket struct {
	Date   time.Time        // local midnight of the day
	Events []calendar.Event // sorted: all-day first, then timed by Start
}

// Group buckets events into one DayBucket per calendar day in [from, to).
// Multi-day events appear in every day they overlap. Within a day: all-day
// events first, then timed events ascending by Start.
//
// `from` must be local midnight; `to` is `from + 3 days`.
func Group(events []calendar.Event, from, to time.Time) []DayBucket {
	const days = 3
	buckets := make([]DayBucket, days)
	for i := 0; i < days; i++ {
		buckets[i] = DayBucket{Date: from.AddDate(0, 0, i)}
	}
	for _, ev := range events {
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
	// Half-open intervals: end-equal-to-start does not overlap.
	return aStart.Before(bEnd) && aEnd.After(bStart)
}

func sortBucket(evs []calendar.Event) {
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].AllDay != evs[j].AllDay {
			return evs[i].AllDay
		}
		return evs[i].Start.Before(evs[j].Start)
	})
}
