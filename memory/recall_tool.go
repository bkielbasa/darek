package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ListTool recalls durable structured memories.
type ListTool struct{ Store *Store }

func (ListTool) Name() string { return "memory.list" }
func (ListTool) Description() string {
	return "Recall durable memories (agreements, events, facts about people) learned from earlier digests and conversations."
}
func (ListTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"query":{"type":"string","description":"keywords over summary/details/people; empty for most recent"},
			"kind":{"type":"string","enum":["agreement","event","person_fact","other"]},
			"status":{"type":"string","enum":["active","done","dismissed","superseded"],"default":"active"},
			"limit":{"type":"integer","minimum":1,"maximum":20,"default":8}
		},
		"required":[]
	}`)
}
func (t ListTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query  string `json:"query"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
		Limit  int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	if p.Status == "" {
		p.Status = "active"
	}
	mems, err := t.Store.ListMemories(ctx, p.Query, p.Kind, p.Status, p.Limit)
	if err != nil {
		return "", err
	}
	if len(mems) == 0 {
		return "no matching memories", nil
	}
	var b strings.Builder
	for _, m := range mems {
		when := "—"
		if m.DueAt != nil {
			when = m.DueAt.Format("2006-01-02")
		}
		fmt.Fprintf(&b, "[%s] %s — %s (%s)\n", when, m.Kind, m.Summary, m.Status)
		if len(m.People) > 0 {
			fmt.Fprintf(&b, "    people: %s\n", strings.Join(m.People, ", "))
		}
	}
	return b.String(), nil
}
