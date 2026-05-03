package serve

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"darek/analyze"
	"darek/links"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type SyncFn func(ctx context.Context) (string, error)

// Analyzer is the subset of *analyze.Analyzer used by the HTTP server.
// Defined as an interface so tests can supply a fake.
type Analyzer interface {
	Analyze(ctx context.Context, in analyze.Input) (analyze.Output, error)
}

// Server is the HTTP UI for browsing and rating links.
type Server struct {
	store   *links.Store
	tmpl    *template.Template
	mux     *http.ServeMux
	sync    SyncFn
	analyze Analyzer
	auth    AuthConfig
}

// New constructs a Server. If sync is nil, the /sync route returns 501.
// If analyzer is nil, /links/{id}/analyze returns 501 and the UI hides the button.
func New(store *links.Store, sync SyncFn, analyzer Analyzer) (*Server, error) {
	t, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{store: store, tmpl: t, mux: http.NewServeMux(), sync: sync, analyze: analyzer}
	s.routes()
	return s, nil
}

// Handler returns an http.Handler suitable for passing to http.Server.
// Wraps the mux with otelhttp for span coverage.
func (s *Server) Handler() http.Handler {
	return otelhttp.NewHandler(s.requireAuth(s.mux), "darek.serve")
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	staticFS, _ := fs.Sub(StaticFS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	s.mux.Handle("GET /{$}", s.handleList(true))  // queue
	s.mux.Handle("GET /all", s.handleList(false)) // archive

	s.mux.HandleFunc("POST /sync", s.handleSync)
	s.mux.HandleFunc("POST /links/new", s.handleNew)
	s.mux.HandleFunc("POST /links/{id}/rating", s.handleRating)
	s.mux.HandleFunc("POST /links/{id}/tags", s.handleTags)
	s.mux.HandleFunc("POST /links/{id}/notes", s.handleNotes)
	s.mux.HandleFunc("POST /links/{id}/kind", s.handleKind)
	s.mux.HandleFunc("POST /links/{id}/analyze", s.handleAnalyze)
}

// Run starts the server on bind and blocks until ctx is canceled.
func (s *Server) Run(ctx context.Context, bind string) error {
	srv := &http.Server{
		Addr:    bind,
		Handler: s.Handler(),
	}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	}
}
