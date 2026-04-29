package serve

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"darek/links"
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
