package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"darek/exechistory"

	"github.com/google/uuid"
)

type executionRowVM struct {
	ID         string
	Kind       string
	Name       string
	StartedAt  string
	DurationMS int64
	Status     string
	IsError    bool
}

type executionsListVM struct {
	PageTitle  string
	Kinds      []string
	Kind       string
	Rows       []executionRowVM
	NextBefore string
	Disabled   bool
}

type stepVM struct {
	Name           string
	DurationMS     int64
	WidthPct       int
	Status         string
	IsError        bool
	Error          string
	Indent         int
	AttributesJSON string
	EventsJSON     string
}

type executionDetailVM struct {
	PageTitle  string
	Exec       exechistory.Execution
	StartedAt  string
	EndedAt    string
	Attributes map[string]any
	Steps      []stepVM
	JaegerURL  string
	Disabled   bool
}

func (s *Server) handleExecutionsList(w http.ResponseWriter, r *http.Request) {
	if s.executions == nil {
		_ = s.tmpl.ExecuteTemplate(w, "executions_list.html", executionsListVM{
			PageTitle: "executions", Disabled: true,
		})
		return
	}
	q := r.URL.Query()
	f := exechistory.ListFilter{Kind: q.Get("kind"), Limit: 50}
	if b := q.Get("before"); b != "" {
		if t, err := time.Parse(time.RFC3339Nano, b); err == nil {
			f.Before = t
		}
	}
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			f.Limit = n
		}
	}
	rows, err := s.executions.List(r.Context(), f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	kinds, err := s.executions.Kinds(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	vm := executionsListVM{
		PageTitle: "executions",
		Kinds:     kinds,
		Kind:      f.Kind,
		Rows:      make([]executionRowVM, 0, len(rows)),
	}
	for _, e := range rows {
		vm.Rows = append(vm.Rows, executionRowVM{
			ID:         e.ID.String(),
			Kind:       e.Kind,
			Name:       e.Name,
			StartedAt:  e.StartedAt.Format("2006-01-02 15:04:05"),
			DurationMS: e.DurationMS,
			Status:     e.Status,
			IsError:    e.Status == "error",
		})
	}
	if len(rows) == f.Limit {
		vm.NextBefore = rows[len(rows)-1].StartedAt.Format(time.RFC3339Nano)
	}
	if err := s.tmpl.ExecuteTemplate(w, "executions_list.html", vm); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleExecutionDetail(w http.ResponseWriter, r *http.Request) {
	if s.executions == nil {
		_ = s.tmpl.ExecuteTemplate(w, "execution_detail.html", executionDetailVM{
			PageTitle: "execution", Disabled: true,
		})
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	exec, steps, err := s.executions.Get(r.Context(), id)
	if errors.Is(err, exechistory.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	vm := executionDetailVM{
		PageTitle:  "execution",
		Exec:       exec,
		StartedAt:  exec.StartedAt.Format("2006-01-02 15:04:05.000"),
		EndedAt:    exec.EndedAt.Format("2006-01-02 15:04:05.000"),
		Attributes: exec.Attributes,
	}
	if s.jaegerURL != "" {
		vm.JaegerURL = fmt.Sprintf("%s/trace/%s", s.jaegerURL, exec.TraceID)
	}
	indent := stepIndents(steps, exec.SpanID)
	for _, sp := range steps {
		width := 0
		if exec.DurationMS > 0 {
			width = int(sp.DurationMS * 100 / exec.DurationMS)
			if width > 100 {
				width = 100
			}
		}
		vm.Steps = append(vm.Steps, stepVM{
			Name:           sp.Name,
			DurationMS:     sp.DurationMS,
			WidthPct:       width,
			Status:         sp.Status,
			IsError:        sp.Status == "error",
			Error:          sp.Error,
			Indent:         indent[sp.SpanID],
			AttributesJSON: jsonString(sp.Attributes),
			EventsJSON:     jsonString(sp.Events),
		})
	}
	if err := s.tmpl.ExecuteTemplate(w, "execution_detail.html", vm); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// stepIndents returns indent depth keyed by span_id, walking the
// parent_span_id chain up to rootSpanID. Unknown parents indent at 1.
func stepIndents(steps []exechistory.Step, rootSpanID string) map[string]int {
	parent := map[string]string{}
	for _, s := range steps {
		parent[s.SpanID] = s.ParentSpanID
	}
	depth := map[string]int{}
	var walk func(string) int
	walk = func(sid string) int {
		if d, ok := depth[sid]; ok {
			return d
		}
		p, ok := parent[sid]
		if !ok || p == "" || p == rootSpanID {
			depth[sid] = 1
			return 1
		}
		depth[sid] = walk(p) + 1
		return depth[sid]
	}
	for _, s := range steps {
		walk(s.SpanID)
	}
	return depth
}

func jsonString(v any) string {
	if v == nil {
		return "null"
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}
