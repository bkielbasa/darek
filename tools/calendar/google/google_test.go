package google

import (
	"testing"
	"time"

	"darek/tools/calendar"

	"github.com/stretchr/testify/require"
	calsvc "google.golang.org/api/calendar/v3"
)

func TestBuildAPIEvent_Timed(t *testing.T) {
	start := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	in := calendar.NewEvent{
		Summary:     "Lunch",
		Description: "with team",
		Location:    "Cafe",
		Start:       start,
		End:         end,
		Attendees:   []string{"a@example.com", "b@example.com"},
	}
	got := buildAPIEvent(in)
	require.Equal(t, "Lunch", got.Summary)
	require.Equal(t, "with team", got.Description)
	require.Equal(t, "Cafe", got.Location)
	require.NotNil(t, got.Start)
	require.Equal(t, start.Format(time.RFC3339), got.Start.DateTime)
	require.Empty(t, got.Start.Date)
	require.Equal(t, end.Format(time.RFC3339), got.End.DateTime)
	require.Len(t, got.Attendees, 2)
	require.Equal(t, "a@example.com", got.Attendees[0].Email)
}

func TestBuildAPIEvent_AllDay(t *testing.T) {
	start := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	in := calendar.NewEvent{Summary: "Holiday", Start: start, End: end, AllDay: true}
	got := buildAPIEvent(in)
	require.Equal(t, "2026-05-02", got.Start.Date)
	require.Empty(t, got.Start.DateTime)
	require.Equal(t, "2026-05-03", got.End.Date)
}

func TestBuildAPIPatch_OnlyPresentFields(t *testing.T) {
	summary := "renamed"
	patch := calendar.EventPatch{Summary: &summary}
	got := buildAPIPatch(patch)
	require.Equal(t, "renamed", got.Summary)
	require.Nil(t, got.Start)
	require.Nil(t, got.End)
	require.Nil(t, got.Attendees)
	require.Contains(t, got.ForceSendFields, "Summary")
}

func TestBuildAPIPatch_StringFieldClearedToEmpty(t *testing.T) {
	empty := ""
	patch := calendar.EventPatch{Description: &empty}
	got := buildAPIPatch(patch)
	require.Equal(t, "", got.Description)
	require.Contains(t, got.ForceSendFields, "Description")
}

func TestBuildAPIPatch_AttendeesCleared(t *testing.T) {
	empty := []string{}
	patch := calendar.EventPatch{Attendees: &empty}
	got := buildAPIPatch(patch)
	require.NotNil(t, got.Attendees)
	require.Empty(t, got.Attendees)
	require.Contains(t, got.ForceSendFields, "Attendees")
}

func TestBuildAPIPatch_TimedStart(t *testing.T) {
	start := time.Date(2026, 5, 2, 15, 0, 0, 0, time.UTC)
	allDay := false
	patch := calendar.EventPatch{Start: &start, AllDay: &allDay}
	got := buildAPIPatch(patch)
	require.NotNil(t, got.Start)
	require.Equal(t, start.Format(time.RFC3339), got.Start.DateTime)
	require.Empty(t, got.Start.Date)
}

func TestBuildAPIPatch_AllDayStart(t *testing.T) {
	start := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	allDay := true
	patch := calendar.EventPatch{Start: &start, AllDay: &allDay}
	got := buildAPIPatch(patch)
	require.NotNil(t, got.Start)
	require.Equal(t, "2026-05-02", got.Start.Date)
	require.Empty(t, got.Start.DateTime)
}

func TestSendUpdates(t *testing.T) {
	require.Equal(t, "all", sendUpdates(true))
	require.Equal(t, "none", sendUpdates(false))
}

func TestConvertCreatedEvent(t *testing.T) {
	api := &calsvc.Event{
		Id:      "abc123",
		Summary: "Lunch",
		Start:   &calsvc.EventDateTime{DateTime: "2026-05-02T12:00:00Z"},
		End:     &calsvc.EventDateTime{DateTime: "2026-05-02T13:00:00Z"},
	}
	ev, ok := convert("work", api)
	require.True(t, ok)
	require.Equal(t, "abc123", ev.UID)
	require.Equal(t, "work", ev.Calendar)
}
