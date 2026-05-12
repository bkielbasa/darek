package serve

import (
	"context"
	"errors"
	"runtime/debug"
	"testing"
	"time"

	"darek/exechistory"
)

func TestPickVersion(t *testing.T) {
	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			name: "module version present",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}},
			want: "v1.2.3",
		},
		{
			name: "module version devel falls back to vcs",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abcdef1234567890"},
				},
			},
			want: "abcdef1",
		},
		{
			name: "no module, no vcs, returns dev",
			info: &debug.BuildInfo{Main: debug.Module{Version: ""}},
			want: "dev",
		},
		{
			name: "nil build info returns dev",
			info: nil,
			want: "dev",
		},
		{
			name: "short vcs revision falls back to dev",
			info: &debug.BuildInfo{
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}},
			},
			want: "dev",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pickVersion(tc.info)
			if got != tc.want {
				t.Errorf("pickVersion = %q, want %q", got, tc.want)
			}
		})
	}
}

// fakeLastSyncLister is a stand-in for *exechistory.Store in tests.
type fakeLastSyncLister struct {
	calls  int
	byKind map[string][]exechistory.Execution
	err    error
}

func (f *fakeLastSyncLister) List(_ context.Context, filter exechistory.ListFilter) ([]exechistory.Execution, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.byKind[filter.Kind], nil
}

func TestLastSyncCache_ReturnsDashWhenListerNil(t *testing.T) {
	c := &lastSyncCache{}
	if got := c.string(nil, time.Now()); got != "—" {
		t.Errorf("got %q, want \"—\"", got)
	}
}

func TestLastSyncCache_PicksNewerOfSyncAndManualSync(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	lister := &fakeLastSyncLister{
		byKind: map[string][]exechistory.Execution{
			"sync":        {{StartedAt: now.Add(-10 * time.Minute)}},
			"manual-sync": {{StartedAt: now.Add(-2 * time.Minute)}}, // newer
		},
	}
	c := &lastSyncCache{}
	got := c.stringWithClock(lister, now, now)
	if got != "2m ago" {
		t.Errorf("got %q, want \"2m ago\"", got)
	}
}

func TestLastSyncCache_TTLPreventsRefetch(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	lister := &fakeLastSyncLister{
		byKind: map[string][]exechistory.Execution{
			"sync": {{StartedAt: now.Add(-3 * time.Minute)}},
		},
	}
	c := &lastSyncCache{}

	_ = c.stringWithClock(lister, now, now)
	if lister.calls != 2 { // one List call per kind tried (sync, manual-sync)
		t.Fatalf("first render: lister.calls = %d, want 2", lister.calls)
	}

	_ = c.stringWithClock(lister, now, now.Add(10*time.Second))
	if lister.calls != 2 {
		t.Errorf("within TTL: lister.calls = %d, want still 2", lister.calls)
	}

	_ = c.stringWithClock(lister, now, now.Add(31*time.Second))
	if lister.calls != 4 {
		t.Errorf("past TTL: lister.calls = %d, want 4", lister.calls)
	}
}

func TestLastSyncCache_DBErrorKeepsLastGoodValue(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	lister := &fakeLastSyncLister{
		byKind: map[string][]exechistory.Execution{
			"sync": {{StartedAt: now.Add(-1 * time.Minute)}},
		},
	}
	c := &lastSyncCache{}
	got := c.stringWithClock(lister, now, now)
	if got != "1m ago" {
		t.Fatalf("seed: got %q, want \"1m ago\"", got)
	}

	lister.err = errors.New("db down")
	got = c.stringWithClock(lister, now, now.Add(lastSyncTTL+time.Second))
	if got != "1m ago" {
		t.Errorf("error path: got %q, want last-good \"1m ago\"", got)
	}
}
