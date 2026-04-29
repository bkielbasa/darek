package serve

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"darek/links"

	"github.com/google/uuid"
)

// linkVM is the view-model for a single row.
type linkVM struct {
	ID            string
	URL           string
	Title         string
	Kind          string
	Feed          string
	Notes         string
	Tags          []string
	Rating        *int
	RelTime       string
	RatingButtons []ratingBtn
	AllKinds      []string
}

type ratingBtn struct {
	Value  int
	Filled bool
}

// indexVM is the view-model for the list page.
type indexVM struct {
	PageTitle string
	Path      string
	Query     listQuery
	Kinds     []string
	Ratings   []int
	Links     []linkVM
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

func toLinkVM(l links.Link) linkVM {
	rb := make([]ratingBtn, 5)
	cur := 0
	if l.Rating != nil {
		cur = *l.Rating
	}
	for i := 0; i < 5; i++ {
		rb[i] = ratingBtn{Value: i + 1, Filled: i < cur}
	}
	return linkVM{
		ID:            l.ID.String(),
		URL:           l.URL,
		Title:         l.Title,
		Kind:          l.Kind,
		Feed:          l.Feed,
		Notes:         l.Notes,
		Tags:          l.Tags,
		Rating:        l.Rating,
		RelTime:       relTime(l.UpdatedAt),
		RatingButtons: rb,
		AllKinds:      []string{"article", "video", "tweet", "podcast", "other"},
	}
}

func relTime(t time.Time) string {
	d := time.Since(t)
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
	if err := s.tmpl.ExecuteTemplate(w, "_rating.html", toLinkVM(cur)); err != nil {
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
			if q.Kind != "" && l.Kind != q.Kind {
				continue
			}
			if q.Feed != "" && l.Feed != q.Feed {
				continue
			}
			rows = append(rows, toLinkVM(l))
		}
		title := "queue"
		path := "/"
		if !queueOnly {
			title = "archive"
			path = "/all"
		}
		vm := indexVM{
			PageTitle: title,
			Path:      path,
			Query:     q,
			Kinds:     []string{"article", "video", "tweet", "podcast", "other"},
			Ratings:   []int{1, 2, 3, 4, 5},
			Links:     rows,
		}
		if err := s.tmpl.ExecuteTemplate(w, "index.html", vm); err != nil {
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
	if err := s.tmpl.ExecuteTemplate(w, "_tags.html", toLinkVM(cur)); err != nil {
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
	if err := s.tmpl.ExecuteTemplate(w, "_notes.html", toLinkVM(cur)); err != nil {
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

	id, _, err := links.IngestOne(r.Context(), s.store, links.Candidate{
		URL:    rawURL,
		Source: "user",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(tags) > 0 {
		// Direct UPDATE — Save's "leave alone if empty" semantics make a
		// generic Save call awkward when the only change is appending tags.
		_, _ = s.store.Pool().Exec(r.Context(),
			`UPDATE links SET tags = ARRAY(SELECT DISTINCT unnest(tags || $2::text[])), updated_at = now() WHERE id = $1`,
			id, tags)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if s.sync == nil {
		http.Error(w, "sync not configured", http.StatusNotImplemented)
		return
	}
	msg, err := s.sync(r.Context())
	if err != nil {
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
	if err := s.tmpl.ExecuteTemplate(w, "_kind.html", toLinkVM(cur)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
