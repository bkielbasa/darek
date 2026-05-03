package serve_test

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"darek/cmd/darek/serve"
)

func dummyAuth(t *testing.T) serve.AuthConfig {
	t.Helper()
	a, err := serve.NewAuthConfig(
		"test",
		[]byte("placeholder-hash"),
		bytes.Repeat([]byte{0}, 32),
		time.Hour,
	)
	if err != nil {
		t.Fatalf("dummy auth: %v", err)
	}
	return a
}

func TestServer_Healthz(t *testing.T) {
	s, err := serve.New(nil, nil, nil, dummyAuth(t))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("status %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body %q, want ok", rec.Body.String())
	}
}

func TestServer_StaticCSS(t *testing.T) {
	s, err := serve.New(nil, nil, nil, dummyAuth(t))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	req := httptest.NewRequest("GET", "/static/style.css", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("status %d, want 200", rec.Code)
	}
}
