package calendar

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeSrc struct {
	name   string
	events []Event
	err    error
}

func (f fakeSrc) Nickname() string { return f.name }
func (f fakeSrc) ListEvents(_ context.Context, _, _ time.Time) ([]Event, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.events, nil
}

func TestSources_ListAll_Sorted(t *testing.T) {
	s := NewSources()
	t1 := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	require.NoError(t, s.Add(fakeSrc{name: "a", events: []Event{{UID: "x", Start: t2}}}))
	require.NoError(t, s.Add(fakeSrc{name: "b", events: []Event{{UID: "y", Start: t1}}}))
	got, err := s.ListEvents(context.Background(), t1, t2.Add(time.Hour), "")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "y", got[0].UID)
	require.Equal(t, "x", got[1].UID)
}

func TestSources_ListByCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "a", events: []Event{{UID: "x"}}}))
	require.NoError(t, s.Add(fakeSrc{name: "b", events: []Event{{UID: "y"}}}))
	got, err := s.ListEvents(context.Background(), time.Time{}, time.Time{}, "b")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "y", got[0].UID)
}

func TestSources_UnknownCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "a"}))
	_, err := s.ListEvents(context.Background(), time.Time{}, time.Time{}, "nope")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown calendar")
}

func TestSources_PartialFailureIgnored(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "ok", events: []Event{{UID: "x"}}}))
	require.NoError(t, s.Add(fakeSrc{name: "bad", err: errors.New("network")}))
	got, err := s.ListEvents(context.Background(), time.Time{}, time.Time{}, "")
	require.NoError(t, err) // not all failed
	require.Len(t, got, 1)
}

func TestSources_AllFailed_Errors(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "bad1", err: errors.New("x")}))
	require.NoError(t, s.Add(fakeSrc{name: "bad2", err: errors.New("y")}))
	_, err := s.ListEvents(context.Background(), time.Time{}, time.Time{}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "all calendar sources failed")
}

func TestSources_Names_Sorted(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "b"}))
	require.NoError(t, s.Add(fakeSrc{name: "a"}))
	require.Equal(t, []string{"a", "b"}, s.Names())
}
