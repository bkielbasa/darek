package blogfeed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClient_List_RSS(t *testing.T) {
	body, err := os.ReadFile("testdata/rss.xml")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/feed.xml", r.URL.Path)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := New(Options{URL: srv.URL + "/feed.xml"})
	require.NoError(t, err)
	entries, err := c.List(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "Newer post", entries[0].Title)
}

func TestClient_List_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c, err := New(Options{URL: srv.URL})
	require.NoError(t, err)
	_, err = c.List(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}
