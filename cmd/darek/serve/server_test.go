package serve_test

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"darek/cmd/darek/serve"

	"github.com/prometheus/client_golang/prometheus"
)

func dummyAuth(t *testing.T) serve.AuthConfig {
	t.Helper()
	a, err := serve.NewAuthConfig(
		bytes.Repeat([]byte{0}, 32),
		time.Hour,
	)
	if err != nil {
		t.Fatalf("dummy auth: %v", err)
	}
	return a
}

func TestServer_Healthz(t *testing.T) {
	s, err := serve.New(nil, nil, nil, dummyAuth(t), &serve.OIDC{}, nil, nil, "", nil)
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
	s, err := serve.New(nil, nil, nil, dummyAuth(t), &serve.OIDC{}, nil, nil, "", nil)
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

func TestServer_Metrics_PublicAndExposes(t *testing.T) {
	reg := prometheus.NewRegistry()
	// Register a known counter so the exposition is guaranteed non-empty.
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "darek_metrics_test_total",
		Help: "test counter",
	})
	reg.MustRegister(counter)
	counter.Inc()

	s, err := serve.New(nil, nil, nil, dummyAuth(t), &serve.OIDC{}, nil, nil, "", reg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status %d, want 200 (auth must be bypassed)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "darek_metrics_test_total") {
		t.Errorf("body missing expected metric line: %q", rec.Body.String())
	}
}
