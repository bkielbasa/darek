package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestICalURL_LiteralURL(t *testing.T) {
	c := CalendarSrc{Nickname: "todoist", URL: "https://example.com/feed.ics"}
	got, err := c.ICalURL()
	require.NoError(t, err)
	require.Equal(t, "https://example.com/feed.ics", got)
}

func TestICalURL_FromEnv(t *testing.T) {
	t.Setenv("ICAL_TEST_URL", "https://todoist.com/ical/secret-token.ics")
	c := CalendarSrc{Nickname: "todoist", URLEnv: "ICAL_TEST_URL"}
	got, err := c.ICalURL()
	require.NoError(t, err)
	require.Equal(t, "https://todoist.com/ical/secret-token.ics", got)
}

func TestICalURL_BothSet_Errors(t *testing.T) {
	c := CalendarSrc{Nickname: "todoist", URL: "x", URLEnv: "Y"}
	_, err := c.ICalURL()
	require.Error(t, err)
	require.Contains(t, err.Error(), "only one of url or url_env")
}

func TestICalURL_NeitherSet_Errors(t *testing.T) {
	c := CalendarSrc{Nickname: "todoist"}
	_, err := c.ICalURL()
	require.Error(t, err)
	require.Contains(t, err.Error(), "url or url_env is required")
}

func TestICalURL_EnvVarEmpty_Errors(t *testing.T) {
	c := CalendarSrc{Nickname: "todoist", URLEnv: "DEFINITELY_NOT_SET_ICAL_VAR"}
	_, err := c.ICalURL()
	require.Error(t, err)
}
