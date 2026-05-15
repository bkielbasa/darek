package mastodon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func newServer(t *testing.T, h http.HandlerFunc) *Client {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Options{Instance: srv.URL, Token: "test-token"})
	require.NoError(t, err)
	return c
}

func TestToot_OK_PostsAndReturnsURL(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/statuses", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.Equal(t, "key-abc", r.Header.Get("Idempotency-Key"))

		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		require.NoError(t, json.Unmarshal(body, &got))
		require.Equal(t, "hello world", got["status"])

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"42","url":"https://fosstodon.org/@bk/42"}`))
	})
	got, err := c.Toot(context.Background(), "hello world", "key-abc")
	require.NoError(t, err)
	require.Equal(t, "42", got.ID)
	require.Equal(t, "https://fosstodon.org/@bk/42", got.URL)
}

func TestToot_NoIdempotencyKey_HeaderOmitted(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","url":"https://example/1"}`))
	})
	_, err := c.Toot(context.Background(), "hi", "")
	require.NoError(t, err)
}

func TestToot_ErrorPropagatesStatusAndBody(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"Text is too long"}`))
	})
	_, err := c.Toot(context.Background(), "way too long", "k")
	require.Error(t, err)
	require.Contains(t, err.Error(), "422")
	require.Contains(t, err.Error(), "too long")
}

func TestVerifyCredentials_OK(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/accounts/verify_credentials", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","username":"bk","acct":"bk","display_name":"BK"}`))
	})
	got, err := c.VerifyCredentials(context.Background())
	require.NoError(t, err)
	require.Equal(t, "bk", got.Username)
	require.Equal(t, "BK", got.DisplayName)
}

func TestVerifyCredentials_Unauthorized(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"token revoked"}`))
	})
	_, err := c.VerifyCredentials(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")
}

func TestNew_RequiresInstanceAndToken(t *testing.T) {
	_, err := New(Options{Token: "t"})
	require.Error(t, err)
	_, err = New(Options{Instance: "https://x"})
	require.Error(t, err)
}
