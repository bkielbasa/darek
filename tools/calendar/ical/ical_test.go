package ical

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const sample = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:e1@test
SUMMARY:Standup
DESCRIPTION:Daily sync
LOCATION:Zoom
DTSTART:20260428T090000Z
DTEND:20260428T093000Z
END:VEVENT
BEGIN:VEVENT
UID:e2@test
SUMMARY:Anniversary
DTSTART;VALUE=DATE:20260501
DTEND;VALUE=DATE:20260502
END:VEVENT
END:VCALENDAR
`

func TestSource_ListEvents_FromHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = w.Write([]byte(sample))
	}))
	defer srv.Close()

	s := New("test", srv.URL)
	from := time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	got, err := s.ListEvents(context.Background(), from, to)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "Standup", got[0].Summary)
	require.False(t, got[0].AllDay)
	require.Equal(t, "Anniversary", got[1].Summary)
	require.True(t, got[1].AllDay)
}

func TestSource_NonOK_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	s := New("test", srv.URL)
	_, err := s.ListEvents(context.Background(), time.Time{}, time.Time{})
	require.Error(t, err)
}
