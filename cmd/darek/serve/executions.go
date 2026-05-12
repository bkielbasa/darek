package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
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
	Page       Page
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
	Page       Page
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
		_ = s.render(w, "executions_list.html", executionsListVM{
			Page:     s.page("executions", "executions · darek"),
			Disabled: true,
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
		Page:  s.page("executions", "executions · darek"),
		Kinds: kinds,
		Kind:  f.Kind,
		Rows:  make([]executionRowVM, 0, len(rows)),
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
	if err := s.render(w, "executions_list.html", vm); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleExecutionDetail(w http.ResponseWriter, r *http.Request) {
	if s.executions == nil {
		_ = s.render(w, "execution_detail.html", executionDetailVM{
			Page:     s.page("executions", "execution · darek"),
			Disabled: true,
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
		Page:       s.page("executions", "execution · darek"),
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
	if err := s.render(w, "execution_detail.html", vm); err != nil {
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

// kindPalette is the fixed list of pastel hexes used for step/row coloring.
// Eight entries — enough for the common known kinds plus a stable fallback
// for unknown ones via FNV-modulo.
var kindPalette = []string{
	"#5f8fa3", // 0 muted teal
	"#a37a5f", // 1 warm tan
	"#7a8c5f", // 2 muted olive
	"#a3805f", // 3 sandstone
	"#7a5fa3", // 4 muted violet
	"#5f9a8c", // 5 sage
	"#a35f8f", // 6 dusty rose
	"#8c8c5f", // 7 muted khaki
}

// knownKinds maps the most common name prefixes to fixed palette indices.
// Adding a new kind here is a one-line change. Aliases (openai → 3, llm → 3)
// keep semantically similar things visually grouped.
var knownKinds = map[string]int{
	"todoist":  0,
	"freshrss": 1,
	"imap":     2,
	"mail":     2,
	"openai":   3,
	"llm":      3,
	"http":     4,
	"darek":    5,
	"serve":    5,
	"calendar": 6,
	"chat":     7,
}

// kindColor returns a stable hex string for a step or execution name.
// The key is the part before the first "." (so "todoist.fetch" → "todoist").
// Names without a dot use the whole string as the key (so "sync" → "sync").
// Known keys hit a fixed palette index; unknown keys hash to a stable
// fallback so the same input always produces the same color.
func kindColor(name string) string {
	key := name
	if i := strings.Index(name, "."); i >= 0 {
		key = name[:i]
	}
	if idx, ok := knownKinds[key]; ok {
		return kindPalette[idx]
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return kindPalette[h.Sum32()%uint32(len(kindPalette))]
}

// formatMS renders a millisecond duration in human-friendly units.
// Used for axis tick labels on the waterfall.
func formatMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms < 60000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	minutes := ms / 60000
	seconds := (ms % 60000) / 1000
	return fmt.Sprintf("%dm %ds", minutes, seconds)
}
