package serve

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"darek/analyze"
	"darek/exechistory"
	"darek/links"
	"darek/obs"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
)

// linkVM is the view-model for a single row.
type linkVM struct {
	ID             string
	URL            string
	Title          string
	Kind           string
	Feed           string
	Notes          string
	Tags           []string
	Rating         *int
	Summary        string
	AnalyzedAt     *time.Time
	AnalyzeEnabled bool
	RelTime        string
	RatingButtons  []ratingBtn
	AllKinds       []string
}

type ratingBtn struct {
	Value  int
	Filled bool
}

// indexVM is the view-model for the list page.
type indexVM struct {
	Page    Page
	Path    string
	Query   listQuery
	Kinds   []string
	Ratings []int
	Links   []linkVM
}

type listQuery struct {
	Q         string
	Kind      string
	MinRating int
	Feed      string
}

func parseListQuery(r *http.Request) listQuery {
	q := listQuery{
		Q:    r.URL.Query().Get("q"),
		Kind: r.URL.Query().Get("kind"),
		Feed: r.URL.Query().Get("feed"),
	}
	if v := r.URL.Query().Get("min_rating"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 5 {
			q.MinRating = n
		}
	}
	return q
}

func toLinkVM(l links.Link, analyzeEnabled bool) linkVM {
	rb := make([]ratingBtn, 5)
	cur := 0
	if l.Rating != nil {
		cur = *l.Rating
	}
	for i := 0; i < 5; i++ {
		rb[i] = ratingBtn{Value: i + 1, Filled: i < cur}
	}
	return linkVM{
		ID:             l.ID.String(),
		URL:            l.URL,
		Title:          l.Title,
		Kind:           l.Kind,
		Feed:           l.Feed,
		Notes:          l.Notes,
		Tags:           l.Tags,
		Rating:         l.Rating,
		Summary:        l.Summary,
		AnalyzedAt:     l.AnalyzedAt,
		AnalyzeEnabled: analyzeEnabled,
		RelTime:        relTime(l.UpdatedAt),
		RatingButtons:  rb,
		AllKinds:       []string{"article", "video", "tweet", "podcast", "other"},
	}
}

func relTime(t time.Time) string {
	return relTimeAt(t, time.Now())
}

// relTimeAt formats t as a relative duration from now. Split out so tests
// can pin "now".
func relTimeAt(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// handleRating sets (or unsets) the rating for a link and returns the
// re-rendered rating widget.
func (s *Server) handleRating(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	value := r.URL.Query().Get("value")
	n, _ := strconv.Atoi(value)

	cur, err := s.fetchOne(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var ratingPtr *int
	// Click-current-to-clear: if the new value equals the existing rating, unset.
	if cur.Rating != nil && *cur.Rating == n {
		ratingPtr = nil
	} else if n >= 1 && n <= 5 {
		v := n
		ratingPtr = &v
	}

	if err := s.setRating(r.Context(), id, ratingPtr); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cur.Rating = ratingPtr
	if err := s.renderPartial(w, "_rating.html", toLinkVM(cur, s.analyze != nil)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// fetchOne reads a single link by id via store.Get.
func (s *Server) fetchOne(ctx context.Context, id uuid.UUID) (links.Link, error) {
	return s.store.Get(ctx, id)
}

// setRating updates only the rating column. Goes through pool directly because
// links.Save's nil-Rating means "leave alone", not "clear".
func (s *Server) setRating(ctx context.Context, id uuid.UUID, rating *int) error {
	_, err := s.store.Pool().Exec(ctx,
		`UPDATE links SET rating = $2, updated_at = now() WHERE id = $1`,
		id, rating)
	return err
}

// handleList serves both the queue (rating IS NULL) and the archive.
func (s *Server) handleList(queueOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := parseListQuery(r)
		opts := links.SearchOpts{
			Query:     q.Q,
			MinRating: q.MinRating,
			Kind:      q.Kind,
			Feed:      q.Feed,
			Limit:     100,
		}
		got, err := s.store.Search(r.Context(), opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var rows []linkVM
		for _, l := range got {
			if queueOnly && l.Rating != nil {
				continue
			}
			rows = append(rows, toLinkVM(l, s.analyze != nil))
		}
		title := "queue"
		path := "/"
		activeKey := "queue"
		if !queueOnly {
			title = "archive"
			path = "/all"
			activeKey = "archive"
		}
		vm := indexVM{
			Page: s.page(activeKey, "darek — "+title),
			Path: path,
			Query:     q,
			Kinds:     []string{"article", "video", "tweet", "podcast", "other"},
			Ratings:   []int{1, 2, 3, 4, 5},
			Links:     rows,
		}
		if err := s.render(w, "index.html", vm); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// handleTags adds or removes tags from a link and returns the re-rendered tags widget.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	tag := strings.TrimSpace(strings.ToLower(r.FormValue("tag")))
	op := r.FormValue("op")
	if tag == "" {
		http.Error(w, "tag required", http.StatusBadRequest)
		return
	}

	switch op {
	case "add":
		_, err = s.store.Pool().Exec(r.Context(),
			`UPDATE links
			   SET tags = ARRAY(SELECT DISTINCT unnest(tags || $2::text[])),
			       updated_at = now()
			 WHERE id = $1`, id, []string{tag})
	case "remove":
		_, err = s.store.Pool().Exec(r.Context(),
			`UPDATE links SET tags = array_remove(tags, $2), updated_at = now() WHERE id = $1`,
			id, tag)
	default:
		http.Error(w, "op must be add|remove", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cur, err := s.fetchOne(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.renderPartial(w, "_tags.html", toLinkVM(cur, s.analyze != nil)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleNotes updates the notes field for a link and returns the re-rendered notes widget.
func (s *Server) handleNotes(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	notes := r.FormValue("notes")

	_, err = s.store.Pool().Exec(r.Context(),
		`UPDATE links SET notes = $2, updated_at = now() WHERE id = $1`,
		id, notes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cur, err := s.fetchOne(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.renderPartial(w, "_notes.html", toLinkVM(cur, s.analyze != nil)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleNew handles the manual-add form: canonicalises and classifies the URL
// via links.IngestOne, optionally applies comma-separated tags, then redirects
// back to the queue.
func (s *Server) handleNew(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	rawURL := strings.TrimSpace(r.FormValue("url"))
	if rawURL == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	tags := splitCSV(r.FormValue("tags"))
	notes := strings.TrimSpace(r.FormValue("notes"))

	id, _, _, err := links.IngestOne(r.Context(), s.store, links.Candidate{
		URL:    rawURL,
		Source: "user",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(tags) > 0 {
		_, _ = s.store.Pool().Exec(r.Context(),
			`UPDATE links SET tags = ARRAY(SELECT DISTINCT unnest(tags || $2::text[])), updated_at = now() WHERE id = $1`,
			id, tags)
	}
	if notes != "" {
		_, _ = s.store.Pool().Exec(r.Context(),
			`UPDATE links SET notes = $2, updated_at = now() WHERE id = $1`,
			id, notes)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if s.sync == nil {
		http.Error(w, "sync not configured", http.StatusNotImplemented)
		return
	}
	ctx, span := otel.Tracer("darek/serve").Start(r.Context(), "serve.manual-sync")
	exechistory.MarkExecution(span, "manual-sync")
	defer span.End()

	msg, err := s.sync(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		fmt.Fprintf(w, "sync failed: %v", err)
		return
	}
	fmt.Fprintf(w, "sync ok: %s", msg)
}

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// handleKind updates the kind field for a link and returns the re-rendered kind widget.
func (s *Server) handleKind(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	kind := r.FormValue("kind")
	switch kind {
	case "article", "video", "tweet", "podcast", "other":
	default:
		http.Error(w, "bad kind", http.StatusBadRequest)
		return
	}
	_, err = s.store.Pool().Exec(r.Context(),
		`UPDATE links SET kind = $2, updated_at = now() WHERE id = $1`,
		id, kind)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cur, err := s.fetchOne(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.renderPartial(w, "_kind.html", toLinkVM(cur, s.analyze != nil)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if s.analyze == nil {
		http.Error(w, "analyze not configured", http.StatusNotImplemented)
		return
	}
	ctx, span := otel.Tracer("darek/serve").Start(r.Context(), "serve.link-analyze")
	exechistory.MarkExecution(span, "link-analyze")
	defer span.End()

	cur, err := s.fetchOne(ctx, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out, err := s.analyze.Analyze(ctx, analyze.Input{
		Title: cur.Title, URL: cur.URL, Body: cur.Summary,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if m, _ := obs.MetricsInstance(); m != nil {
			m.LinksAnalyze.Add(ctx, 1, metric.WithAttributes(
				attribute.String("outcome", "error"),
				attribute.String("trigger", "manual"),
			))
		}
		// Render the row with an inline error in the summary slot.
		cur.Summary = fmt.Sprintf("analysis failed: %v", err)
		_ = s.renderPartial(w, "_row.html", toLinkVM(cur, true))
		return
	}

	if err := s.store.ApplyAnalysis(ctx, id, out.Summary, out.Tags); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if m, _ := obs.MetricsInstance(); m != nil {
		m.LinksAnalyze.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", "ok"),
			attribute.String("trigger", "manual"),
		))
	}

	cur, err = s.fetchOne(ctx, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.renderPartial(w, "_row.html", toLinkVM(cur, true)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
