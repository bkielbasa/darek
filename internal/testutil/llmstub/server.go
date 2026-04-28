package llmstub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Reply describes one canned chat-completions response, returned in order of script.
type Reply struct {
	StatusCode int                    // 0 → 200
	Body       map[string]interface{} // raw JSON to return
}

type Server struct {
	*httptest.Server
	mu     sync.Mutex
	script []Reply
	calls  []map[string]interface{}
}

func New(t *testing.T, script ...Reply) *Server {
	t.Helper()
	s := &Server{script: append([]Reply{}, script...)}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) Calls() []map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]interface{}, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.calls = append(s.calls, body)
	if len(s.script) == 0 {
		s.mu.Unlock()
		http.Error(w, "no scripted reply", http.StatusInternalServerError)
		return
	}
	reply := s.script[0]
	s.script = s.script[1:]
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if reply.StatusCode != 0 {
		w.WriteHeader(reply.StatusCode)
	}
	_ = json.NewEncoder(w).Encode(reply.Body)
}
