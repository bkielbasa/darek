package blogfeed

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParse_RSS(t *testing.T) {
	raw, err := os.ReadFile("testdata/rss.xml")
	require.NoError(t, err)

	entries, err := Parse(raw)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Newest first.
	require.Equal(t, "Newer post", entries[0].Title)
	require.Equal(t, "https://blog.example.com/newer?utm_source=feed", entries[0].URL)
	require.Equal(t, "https://blog.example.com/newer", entries[0].CanonicalURL)
	require.Equal(t, "Hello world summary.", entries[0].Summary)
	require.Equal(t, time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC), entries[0].PublishedAt.UTC())

	require.Equal(t, "Older post", entries[1].Title)
	require.Equal(t, "https://blog.example.com/older", entries[1].CanonicalURL)
}

func TestParse_Atom(t *testing.T) {
	raw, err := os.ReadFile("testdata/atom.xml")
	require.NoError(t, err)

	entries, err := Parse(raw)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "Atom post", entries[0].Title)
	require.Equal(t, "https://blog.example.com/atom-post", entries[0].URL)
	require.Equal(t, "https://blog.example.com/atom-post", entries[0].CanonicalURL)
	require.Equal(t, "Atom summary.", entries[0].Summary)
	require.Equal(t, time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC), entries[0].PublishedAt.UTC())
}

func TestParse_RSS_FallsBackToDCDate(t *testing.T) {
	raw, err := os.ReadFile("testdata/rss_no_pubdate.xml")
	require.NoError(t, err)
	entries, err := Parse(raw)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, time.Date(2026, 5, 3, 8, 0, 0, 0, time.UTC), entries[0].PublishedAt.UTC())
}

func TestParse_UnknownRoot(t *testing.T) {
	_, err := Parse([]byte(`<?xml version="1.0"?><weird/>`))
	require.Error(t, err)
}
