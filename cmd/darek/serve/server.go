package serve

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"darek/links"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Server is the HTTP UI for browsing and rating links.
type Server struct {
	store *links.Store
	tmpl  *template.Template
	mux   *http.ServeMux
}

// New constructs a Server. Templates are parsed once at construction time.
func New(store *links.Store) (*Server, error) {
	t, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{store: store, tmpl: t, mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

// Handler returns an http.Handler suitable for passing to http.Server.
// Wraps the mux with otelhttp for span coverage.
func (s *Server) Handler() http.Handler {
	return otelhttp.NewHandler(s.mux, "darek.serve")
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

	s.mux.HandleFunc("POST /links/{id}/rating", s.handleRating)
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
