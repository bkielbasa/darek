package calendar

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestListEventsTool_DefaultsAndFormatting(t *testing.T) {
	s := NewSources()
	now := time.Now()
	require.NoError(t, s.Add(fakeSrc{
		name: "personal",
		events: []Event{
			{Calendar: "personal", Summary: "Meeting", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Location: "Office"},
			{Calendar: "personal", Summary: "Lunch", Start: now.Add(3 * time.Hour), End: now.Add(4 * time.Hour)},
		},
	}))
	out, err := ListEventsTool{Sources: s}.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Contains(t, out, "[personal]")
	require.Contains(t, out, "Meeting @ Office")
	require.Contains(t, out, "Lunch")
}

func TestListEventsTool_NoEvents(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "p"}))
	out, err := ListEventsTool{Sources: s}.Execute(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "no events in window", out)
}

func TestListEventsTool_BadFrom(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "p"}))
	_, err := ListEventsTool{Sources: s}.Execute(context.Background(), json.RawMessage(`{"from":"bogus"}`))
	require.Error(t, err)
}

func TestCreateEventTool_HappyPath(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{
		"calendar":"work",
		"summary":"Lunch",
		"start":"2026-05-02T12:00:00+02:00",
		"end":"2026-05-02T13:00:00+02:00",
		"location":"Cafe",
		"attendees":["a@example.com"],
		"send_invites":true
	}`)
	out, err := CreateEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Contains(t, out, "[work]")
	require.Contains(t, out, "Lunch")
	require.Contains(t, out, "uid: new-uid")

	require.Len(t, w.created, 1)
	got := w.created[0]
	require.Equal(t, "Lunch", got.Summary)
	require.Equal(t, "Cafe", got.Location)
	require.Equal(t, []string{"a@example.com"}, got.Attendees)
	require.True(t, got.SendInvites)
	require.False(t, got.AllDay)
}

func TestCreateEventTool_DefaultEndTimedIsOneHour(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","summary":"Quick","start":"2026-05-02T15:00:00Z"}`)
	_, err := CreateEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Len(t, w.created, 1)
	require.Equal(t, time.Hour, w.created[0].End.Sub(w.created[0].Start))
}

func TestCreateEventTool_AllDayDefaultEndIsOneDay(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","summary":"Holiday","start":"2026-05-02","all_day":true}`)
	_, err := CreateEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Len(t, w.created, 1)
	require.True(t, w.created[0].AllDay)
	require.Equal(t, 24*time.Hour, w.created[0].End.Sub(w.created[0].Start))
}

func TestCreateEventTool_ReadOnlyCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "feed"}))
	args := json.RawMessage(`{"calendar":"feed","summary":"x","start":"2026-05-02T15:00:00Z"}`)
	_, err := CreateEventTool{Sources: s}.Execute(context.Background(), args)
	require.Error(t, err)
	require.Contains(t, err.Error(), "read-only")
}

func TestCreateEventTool_ValidationErrors(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	cases := map[string]string{
		"empty summary":         `{"calendar":"work","summary":"","start":"2026-05-02T15:00:00Z"}`,
		"missing summary":       `{"calendar":"work","start":"2026-05-02T15:00:00Z"}`,
		"missing calendar":      `{"summary":"x","start":"2026-05-02T15:00:00Z"}`,
		"missing start":         `{"calendar":"work","summary":"x"}`,
		"end before start":      `{"calendar":"work","summary":"x","start":"2026-05-02T15:00:00Z","end":"2026-05-02T14:00:00Z"}`,
		"bad rfc3339":           `{"calendar":"work","summary":"x","start":"not-a-time"}`,
		"all_day with time":     `{"calendar":"work","summary":"x","start":"2026-05-02T15:00:00Z","all_day":true}`,
		"non-all-day with date": `{"calendar":"work","summary":"x","start":"2026-05-02"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := CreateEventTool{Sources: s}.Execute(context.Background(), json.RawMessage(body))
			require.Error(t, err)
			require.Empty(t, w.created)
		})
	}
}

func TestUpdateEventTool_PartialPatch(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","uid":"abc","summary":"renamed"}`)
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Len(t, w.updates, 1)
	require.Equal(t, "abc", w.updates[0].UID)
	require.NotNil(t, w.updates[0].Patch.Summary)
	require.Equal(t, "renamed", *w.updates[0].Patch.Summary)
	require.Nil(t, w.updates[0].Patch.Description)
	require.Nil(t, w.updates[0].Patch.Attendees)
	require.Nil(t, w.updates[0].Patch.Start)
}

func TestUpdateEventTool_AttendeesPresenceClearVsAbsent(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	// Absent — no change
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc","summary":"x"}`))
	require.NoError(t, err)
	require.Nil(t, w.updates[0].Patch.Attendees)

	// Present empty — clear all
	_, err = UpdateEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc","attendees":[]}`))
	require.NoError(t, err)
	require.NotNil(t, w.updates[1].Patch.Attendees)
	require.Empty(t, *w.updates[1].Patch.Attendees)

	// Present with values — replace
	_, err = UpdateEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc","attendees":["a@example.com"]}`))
	require.NoError(t, err)
	require.NotNil(t, w.updates[2].Patch.Attendees)
	require.Equal(t, []string{"a@example.com"}, *w.updates[2].Patch.Attendees)
}

func TestUpdateEventTool_NoFields(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "no fields to update")
	require.Empty(t, w.updates)
}

func TestUpdateEventTool_StartEndBothPresentValidates(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	bad := json.RawMessage(`{"calendar":"work","uid":"abc","start":"2026-05-02T15:00:00Z","end":"2026-05-02T14:00:00Z"}`)
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(), bad)
	require.Error(t, err)
	require.Contains(t, err.Error(), "end must not be before start")
}

func TestUpdateEventTool_StartOnlyPatchSkipsCompare(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","uid":"abc","start":"2026-05-02T15:00:00Z"}`)
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Len(t, w.updates, 1)
	require.NotNil(t, w.updates[0].Patch.Start)
	require.Nil(t, w.updates[0].Patch.End)
}

func TestUpdateEventTool_ReadOnlyCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "feed"}))
	args := json.RawMessage(`{"calendar":"feed","uid":"abc","summary":"x"}`)
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(), args)
	require.Error(t, err)
	require.Contains(t, err.Error(), "read-only")
}

func TestUpdateEventTool_SendInvitesRoundTrips(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc","summary":"x","send_invites":true}`))
	require.NoError(t, err)
	require.True(t, w.updates[0].Patch.SendInvites)
}

func TestUpdateEventTool_SendInvitesAloneIsNotAPatchField(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","uid":"abc","send_invites":true}`)
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(), args)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no fields to update")
	require.Empty(t, w.updates)
}

func TestUpdateEventTool_MalformedStart(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","uid":"abc","start":"not-a-time"}`)
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(), args)
	require.Error(t, err)
	require.Empty(t, w.updates)
}

func TestUpdateEventTool_AllDayWithDatetime(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","uid":"abc","all_day":true,"start":"2026-05-02T15:00:00Z"}`)
	_, err := UpdateEventTool{Sources: s}.Execute(context.Background(), args)
	require.Error(t, err)
	require.Contains(t, err.Error(), "all_day requires YYYY-MM-DD")
	require.Empty(t, w.updates)
}

func TestDeleteEventTool_HappyPath(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	args := json.RawMessage(`{"calendar":"work","uid":"abc","send_invites":true}`)
	out, err := DeleteEventTool{Sources: s}.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Equal(t, `deleted: abc from work`, out)

	require.Len(t, w.deletes, 1)
	require.Equal(t, "abc", w.deletes[0].UID)
	require.True(t, w.deletes[0].SendInvites)
}

func TestDeleteEventTool_DefaultSendInvitesFalse(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	_, err := DeleteEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"work","uid":"abc"}`))
	require.NoError(t, err)
	require.False(t, w.deletes[0].SendInvites)
}

func TestDeleteEventTool_RequiredFields(t *testing.T) {
	s := NewSources()
	w := &fakeWritableSrc{fakeSrc: fakeSrc{name: "work"}}
	require.NoError(t, s.Add(w))

	for name, body := range map[string]string{
		"missing calendar": `{"uid":"abc"}`,
		"missing uid":      `{"calendar":"work"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DeleteEventTool{Sources: s}.Execute(context.Background(), json.RawMessage(body))
			require.Error(t, err)
		})
	}
	require.Empty(t, w.deletes)
}

func TestDeleteEventTool_ReadOnlyCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "feed"}))
	_, err := DeleteEventTool{Sources: s}.Execute(context.Background(),
		json.RawMessage(`{"calendar":"feed","uid":"abc"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "read-only")
}
