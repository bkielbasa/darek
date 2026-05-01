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

// fakeWritableSrc embeds fakeSrc behaviour and records write calls.
type fakeWritableSrc struct {
	fakeSrc
	created []NewEvent
	updates []struct {
		UID   string
		Patch EventPatch
	}
	deletes []struct {
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
