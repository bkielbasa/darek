// Package digest renders a 3-day calendar digest into plaintext + HTML.
package digest

import "time"

// Window returns the [from, to) bounds of the digest window: today's local
// midnight through start of the day after tomorrow + 1 (i.e. 3 calendar
// days starting at the local midnight of `now`). The TZ of the returned
// times is the TZ of `now`.
func Window(now time.Time) (from, to time.Time) {
	from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to = from.AddDate(0, 0, 3)
	return from, to
}
