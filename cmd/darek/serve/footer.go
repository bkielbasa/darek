package serve

import (
	"context"
	"runtime/debug"
	"sync"
	"time"

	"darek/exechistory"
)

// pickVersion picks a human-friendly version string from build info.
// Prefers info.Main.Version (when set and not "(devel)"), then the first 7
// chars of vcs.revision, then "dev".
//
// Split out from buildVersion so tests can drive the logic without
// monkey-patching debug.ReadBuildInfo.
func pickVersion(info *debug.BuildInfo) string {
	if info != nil {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return s.Value[:7]
			}
		}
	}
	return "dev"
}

// buildVersion reads the running binary's build info and returns a short
// version string. Called once at server startup.
func buildVersion() string {
	info, _ := debug.ReadBuildInfo()
	return pickVersion(info)
}

// lastSyncLister is the subset of *exechistory.Store the footer uses.
// Defined as an interface so tests can substitute a fake.
type lastSyncLister interface {
	List(ctx context.Context, f exechistory.ListFilter) ([]exechistory.Execution, error)
}

const lastSyncTTL = 30 * time.Second

// lastSyncCache memoises the formatted "last sync" string for lastSyncTTL.
// Zero value is ready to use.
type lastSyncCache struct {
	mu      sync.Mutex
	fetched time.Time // wall-clock time of the last successful fetch
	value   string    // formatted relative string; "" when no fetch has succeeded yet
}

// string returns the cached value, refreshing from lister if the entry is
// stale. nil lister always returns "—".
func (c *lastSyncCache) string(lister lastSyncLister, now time.Time) string {
	return c.stringWithClock(lister, now, now)
}

// stringWithClock is the testable form: the caller passes both "now" and
// "wall" so tests can advance time without sleeping. now is used for relTime
// formatting; wall is the timestamp recorded for TTL accounting.
func (c *lastSyncCache) stringWithClock(lister lastSyncLister, now, wall time.Time) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.fetched.IsZero() && wall.Sub(c.fetched) < lastSyncTTL {
		return c.value
	}
	if lister == nil {
		c.fetched = wall
		c.value = "—"
		return c.value
	}

	// We hold c.mu across the DB call. lastSyncTTL bounds the refresh rate,
	// and the per-call timeout below bounds any single render's wait — so
	// in the worst case a render stalls 2s, not until a hung query returns.
	// For darek's single-user workload this is fine; if concurrency grows,
	// move the I/O out of the critical section with a single-flight pattern.
	var newest time.Time
	ok := false
	for _, kind := range []string{"sync", "manual-sync"} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		rows, err := lister.List(ctx, exechistory.ListFilter{Kind: kind, Limit: 1})
		cancel()
		if err != nil {
			// DB error on any kind aborts the whole refresh. Keep the
			// last-good value and do NOT advance c.fetched so the next
			// render retries instead of waiting out the TTL. If we never
			// had a good value, surface "—" so the footer isn't blank.
			if c.value == "" {
				return "—"
			}
			return c.value
		}
		if len(rows) > 0 && rows[0].StartedAt.After(newest) {
			newest = rows[0].StartedAt
			ok = true
		}
	}

	val := "—"
	if ok {
		val = relTimeAt(newest, now)
	}
	c.fetched = wall
	c.value = val
	return val
}

// footerInfo returns the FooterInfo for a render. version is the cached
// value computed at startup; lastSync is refreshed on a 30-second TTL.
func (s *Server) footerInfo() FooterInfo {
	var lister lastSyncLister
	if s.executions != nil {
		lister = s.executions
	}
	return FooterInfo{
		Brand:    "darek",
		Version:  s.version,
		LastSync: s.lastSync.string(lister, time.Now()),
	}
}
