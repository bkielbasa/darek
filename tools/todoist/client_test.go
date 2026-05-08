package todoist

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Options{Token: "test-token", BaseURL: srv.URL})
	require.NoError(t, err)
	return c, srv
}

func TestListTasks_WithFilter_UsesFilterEndpoint(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.Equal(t, "/tasks/filter", r.URL.Path)
		require.Equal(t, "today", r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"1","content":"Buy milk","priority":3}],"next_cursor":null}`))
	})
	got, err := c.ListTasks(context.Background(), ListFilter{Filter: "today"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "Buy milk", got[0].Content)
}

func TestListTasks_NoFilter_UsesPlainTasks(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/tasks", r.URL.Path)
		require.Empty(t, r.URL.Query().Get("query"))
		_, _ = w.Write([]byte(`{"results":[{"id":"2","content":"X"}]}`))
	})
	got, err := c.ListTasks(context.Background(), ListFilter{})
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestCreateTask(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/tasks", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		require.NoError(t, json.Unmarshal(body, &got))
		require.Equal(t, "Call mom", got["content"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"42","content":"Call mom"}`))
	})
	got, err := c.CreateTask(context.Background(), CreateRequest{Content: "Call mom"})
	require.NoError(t, err)
	require.Equal(t, "42", got.ID)
}

func TestCompleteTask(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.True(t, strings.HasSuffix(r.URL.Path, "/tasks/42/close"))
		w.WriteHeader(204)
	})
	require.NoError(t, c.CompleteTask(context.Background(), "42"))
}

func TestUpdateTask_PartialFields(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.Contains(t, string(body), `"priority":4`)
		require.NotContains(t, string(body), `"content"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"42","content":"Call mom","priority":4}`))
	})
	pri := 4
	_, err := c.UpdateTask(context.Background(), "42", UpdateRequest{Priority: &pri})
	require.NoError(t, err)
}

func TestErrorPropagation(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte("unauthorized"))
	})
	_, err := c.ListTasks(context.Background(), ListFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")
}

func TestListProjects_Paginated(t *testing.T) {
	calls := 0
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/projects", r.URL.Path)
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"results":[{"id":"100","name":"Inbox"},{"id":"200","name":"Marketing"}],"next_cursor":"NEXT"}`))
			return
		}
		require.Equal(t, "NEXT", r.URL.Query().Get("cursor"))
		_, _ = w.Write([]byte(`{"results":[{"id":"300","name":"Side"}]}`))
	})
	got, err := c.ListProjects(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, calls, "should follow pagination")
	require.Len(t, got, 3)
	require.Equal(t, "Marketing", got[1].Name)
}

func TestResolveProjectID_HitAndMiss(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"id":"42","name":"Marketing"}]}`))
	})
	id, err := c.ResolveProjectID(context.Background(), "Marketing")
	require.NoError(t, err)
	require.Equal(t, "42", id)

	_, err = c.ResolveProjectID(context.Background(), "Nope")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Nope")
}
