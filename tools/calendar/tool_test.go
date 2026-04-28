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
