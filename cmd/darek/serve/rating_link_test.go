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
	pgtest "darek/internal/testutil/pg"
	"darek/links"

	"github.com/google/uuid"
)

func newTestServerForRating(t *testing.T) (*Server, *db.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, raw := pgtest.Start(t)
	if err := db.Migrate(ctx, raw); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool := db.Wrap(raw)

	authCfg, err := NewAuthConfig([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	srv, err := New(links.NewStore(pool), nil, nil, authCfg, &OIDC{}, nil, nil, "", nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return srv, pool
}

func seedLink(t *testing.T, pool *db.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO links (id, url, title) VALUES ($1, $2, 'Rust ownership')`,
		id, "https://e.com/"+id.String())
	if err != nil {
		t.Fatalf("seed link: %v", err)
	}
	return id
}

func linkRating(t *testing.T, pool *db.Pool, id uuid.UUID) *int {
	t.Helper()
	var rating *int
	err := pool.QueryRow(context.Background(),
		`SELECT rating FROM links WHERE id = $1`, id).Scan(&rating)
	if err != nil {
		t.Fatalf("read rating: %v", err)
	}
	return rating
}

func TestHandleRateLink_SetsRatingPublicly(t *testing.T) {
	srv, pool := newTestServerForRating(t)
	id := seedLink(t, pool)

	// No session cookie — the route must still work (email clients).
	req := httptest.NewRequest("GET", "/r/"+id.String()+"/3", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := linkRating(t, pool, id); got == nil || *got != 3 {
		t.Fatalf("rating = %v, want 3", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "★★★☆☆") {
		t.Errorf("body missing stars: %q", body)
	}
	if !strings.Contains(body, "Rust ownership") {
		t.Errorf("body missing title: %q", body)
	}
}

func TestHandleRateLink_SetsNotToggles(t *testing.T) {
	srv, pool := newTestServerForRating(t)
	id := seedLink(t, pool)

	for _, v := range []string{"3", "3", "5"} {
		req := httptest.NewRequest("GET", "/r/"+id.String()+"/"+v, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET value %s: status %d", v, rec.Code)
		}
	}
	if got := linkRating(t, pool, id); got == nil || *got != 5 {
		t.Fatalf("rating = %v, want 5 (re-click must not clear)", got)
	}
}

func TestHandleRateLink_BadInput(t *testing.T) {
	srv, pool := newTestServerForRating(t)
	id := seedLink(t, pool)

	for _, path := range []string{
		"/r/not-a-uuid/3",
		"/r/" + id.String() + "/0",
		"/r/" + id.String() + "/6",
		"/r/" + id.String() + "/x",
	} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", path, rec.Code)
		}
	}
	if got := linkRating(t, pool, id); got != nil {
		t.Fatalf("rating = %v, want unset after bad requests", got)
	}

	req := httptest.NewRequest("GET", "/r/"+uuid.NewString()+"/3", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status %d, want 404", rec.Code)
	}
}
