package freshrss

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func newServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Options{BaseURL: srv.URL, Username: "u", Password: "p"})
	require.NoError(t, err)
	return c, srv
}

func TestLogin_AndList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/greader.php/accounts/ClientLogin", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("SID=abcdef\nLSID=ignored\nAuth=ignored\n"))
	})
	mux.HandleFunc("/api/greader.php/reader/api/0/stream/contents/", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GoogleLogin auth=abcdef", r.Header.Get("Authorization"))
		require.Equal(t, "user/-/state/com.google/read", r.URL.Query().Get("xt"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"items":[{
				"id":"tag:google.com,2005:reader/item/aaaa",
				"title":"Hello",
				"published":1700000000,
				"categories":["user/-/state/com.google/reading-list","user/-/state/com.google/starred"],
				"canonical":[{"href":"https://x.com/a"}],
				"summary":{"content":"<p>body</p>"},
				"origin":{"title":"My Feed"}
			}]
		}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, err := New(Options{BaseURL: srv.URL, Username: "u", Password: "p"})
	require.NoError(t, err)

	got, err := c.List(context.Background(), ListOpts{Filter: FilterUnread})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "Hello", got[0].Title)
	require.Equal(t, "https://x.com/a", got[0].URL)
	require.True(t, got[0].Starred)
	require.Equal(t, "My Feed", got[0].Feed)
}

func TestList_Starred_UsesStarredStream(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/greader.php/accounts/ClientLogin", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("SID=zzz\n"))
	})
	mux.HandleFunc("/api/greader.php/reader/api/0/stream/contents/", func(w http.ResponseWriter, r *http.Request) {
		rawPath := r.URL.RawPath
		if rawPath == "" {
			rawPath = r.URL.Path
		}
		require.Contains(t, rawPath, "user%2F-%2Fstate%2Fcom.google%2Fstarred")
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL, Username: "u", Password: "p"})
	_, err := c.List(context.Background(), ListOpts{Filter: FilterStarred})
	require.NoError(t, err)
}

func TestMark_PostsCorrectFlags(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/greader.php/accounts/ClientLogin", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("SID=zzz\n"))
	})
	mux.HandleFunc("/api/greader.php/reader/api/0/token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("TOK"))
	})
	var seen string
	mux.HandleFunc("/api/greader.php/reader/api/0/edit-tag", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = string(body)
		w.WriteHeader(200)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL, Username: "u", Password: "p"})
	require.NoError(t, c.Mark(context.Background(), "tag:google.com,2005:reader/item/abc", ActionMarkRead))
	require.Contains(t, seen, "T=TOK")
	require.Contains(t, seen, "a=user%2F-%2Fstate%2Fcom.google%2Fread")
	require.Contains(t, seen, "i=tag%3Agoogle.com%2C2005%3Areader%2Fitem%2Fabc")
}

func TestList_BadStatus_Errors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/greader.php/accounts/ClientLogin", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("SID=zzz\n"))
	})
	mux.HandleFunc("/api/greader.php/reader/api/0/stream/contents/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL, Username: "u", Password: "p"})
	_, err := c.List(context.Background(), ListOpts{})
	require.Error(t, err)
}
