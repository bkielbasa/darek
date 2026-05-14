//go:build integration

package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"darek/db"
	"darek/exechistory"
	pgtest "darek/internal/testutil/pg"

	"github.com/google/uuid"
)

func newTestServerWithExecutions(t *testing.T) (*Server, *db.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, raw := pgtest.Start(t)
	if err := db.Migrate(ctx, raw); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool := db.Wrap(raw)
	store := exechistory.NewStore(pool)

	authCfg, err := NewAuthConfig([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	srv, err := New(nil, nil, nil, authCfg, &OIDC{}, nil, store, "")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return srv, pool
}

func seedExecution(t *testing.T, pool *db.Pool, kind string, startedAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := pool.Exec(context.Background(), `
INSERT INTO executions (id, trace_id, span_id, kind, name, started_at, ended_at, duration_ms, status, attributes)
VALUES ($1,'t',$2,$3,'n',$4,$4,0,'ok','{}'::jsonb)`,
		id, uuid.NewString(), kind, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHandleExecutionsList_FiltersByKind(t *testing.T) {
	srv, pool := newTestServerWithExecutions(t)
	now := time.Now().UTC()
	_ = seedExecution(t, pool, "freshrss-sync", now)
	_ = seedExecution(t, pool, "chat-turn", now.Add(time.Second))

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/executions?kind=freshrss-sync", nil)
	srv.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "freshrss-sync") {
		t.Errorf("body missing freshrss-sync: %s", body)
	}
	// chat-turn shows up in the kind dropdown (all distinct kinds are listed
	// for selection); the filter should keep it out of the table body. Count
	// rows by counting <tr class="..."> in the tbody — there should be one
	// freshrss-sync row and zero chat-turn rows.
	if n := strings.Count(body, "<td>chat-turn</td>"); n != 0 {
		t.Errorf("expected 0 chat-turn table rows after kind filter, got %d", n)
	}
	if n := strings.Count(body, "<td>freshrss-sync</td>"); n != 1 {
		t.Errorf("expected 1 freshrss-sync table row, got %d", n)
	}
}

func TestHandleExecutionDetail_RendersSummaryAndSteps(t *testing.T) {
	srv, pool := newTestServerWithExecutions(t)
	id := seedExecution(t, pool, "freshrss-sync", time.Now().UTC())

	_, err := pool.Exec(context.Background(), `
INSERT INTO execution_steps (id, execution_id, parent_span_id, span_id, name, started_at, ended_at, duration_ms, status, attributes, events)
VALUES ($1,$2,'0123456789abcdef','a','fetch',$3,$3,42,'ok','{}'::jsonb,'[]'::jsonb)`,
		uuid.New(), id, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/executions/"+id.String(), nil)
	srv.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "freshrss-sync") || !strings.Contains(body, "fetch") {
		t.Errorf("body missing pieces: %s", body)
	}
}

func TestHandleExecutionsList_DisabledRendersFriendlyMessage(t *testing.T) {
	authCfg, err := NewAuthConfig([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(nil, nil, nil, authCfg, &OIDC{}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/executions", nil)
	srv.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "disabled") {
		t.Error("expected 'disabled' message in body")
	}
}
