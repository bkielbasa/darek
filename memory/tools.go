package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type RecallTool struct{ Store *Store }

func (RecallTool) Name() string        { return "memory.recall" }
func (RecallTool) Description() string { return "Recall personal notes saved in earlier conversations. Returns up to N notes." }
func (RecallTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"query":{"type":"string","description":"keywords to search for; empty for most recent"},
			"limit":{"type":"integer","minimum":1,"maximum":20,"default":5}
		},
		"required":[]
	}`)
}
func (rt RecallTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	notes, err := rt.Store.Recall(ctx, p.Query, p.Limit)
	if err != nil {
		return "", err
	}
	if len(notes) == 0 {
		return "no matching notes", nil
	}
	var b strings.Builder
	for i, n := range notes {
		fmt.Fprintf(&b, "[%d] %s — %s\n", i+1, n.CreatedAt.Format("2006-01-02"), n.Body)
		if len(n.Tags) > 0 {
			fmt.Fprintf(&b, "    tags: %s\n", strings.Join(n.Tags, ", "))
		}
	}
	return b.String(), nil
}

type SaveTool struct{ Store *Store }

func (SaveTool) Name() string        { return "memory.save" }
func (SaveTool) Description() string { return "Save a note for future conversations. Use when the user shares a fact you should remember." }
func (SaveTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"body":{"type":"string","description":"the note content"},
			"tags":{"type":"array","items":{"type":"string"},"description":"optional tags"}
		},
		"required":["body"]
	}`)
}
func (st SaveTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Body string   `json:"body"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if p.Body == "" {
		return "", fmt.Errorf("body required")
	}
	id, err := st.Store.Save(ctx, p.Body, p.Tags, "agent_save")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("saved note %s", id), nil
}
