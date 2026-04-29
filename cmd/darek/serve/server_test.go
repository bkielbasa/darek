package serve_test

import (
	"net/http/httptest"
	"testing"

	"darek/cmd/darek/serve"
)

func TestServer_Healthz(t *testing.T) {
	s, err := serve.New(nil) // healthz doesn't need a store
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
	s, err := serve.New(nil)
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
