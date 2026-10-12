package recommend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// LLM combines the embedding and chat capabilities the recommender needs.
// *llm.Client satisfies it.
type LLM interface {
	Embedder
	Chat
}

// Tool recommends links to read via the chat agent.
type Tool struct {
	store  *Store
	client LLM
	opts   Options
}

// NewTool constructs a links.recommend tool.
func NewTool(store *Store, client LLM, opts Options) *Tool {
	return &Tool{store: store, client: client, opts: opts}
}

func (Tool) Name() string { return "links.recommend" }
func (Tool) Description() string {
	return "Recommend unread links to read next, based on the user's historical ratings. " +
		"Returns up to a few suggestions with a short reason each. Never repeats earlier " +
		"suggestions unless include_seen is set."
}
func (Tool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"count":{"type":"integer","minimum":1,"maximum":10,"default":3,"description":"how many suggestions to return"},
			"kind":{"type":"string","enum":["article","video"],"description":"restrict to one kind"},
			"include_seen":{"type":"boolean","default":false,"description":"allow links recommended before"}
		},
		"required":[]
	}`)
}

func (t Tool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.store == nil || t.client == nil {
		return "", fmt.Errorf("recommender not configured")
	}
	var p struct {
		Count       int    `json:"count"`
		Kind        string `json:"kind"`
		IncludeSeen bool   `json:"include_seen"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	opts := t.opts
	if p.Count > 0 {
		opts.Count = p.Count
	}
	if opts.Count <= 0 {
		opts.Count = 3
	}
	opts.Kind = p.Kind
	opts.IncludeSeen = p.IncludeSeen

	res, err := Build(ctx, t.store, t.client, NewSelector(t.client), opts, "chat", nil)
	if err != nil {
		return "", err
	}
	if len(res.Suggestions) == 0 {
		return "no reading suggestions right now (rate a few links first)", nil
	}
	var b strings.Builder
	for _, s := range res.Suggestions {
		fmt.Fprintf(&b, "%s (%s)\n", displayTitle(s), s.Kind)
		if s.Reason != "" {
			fmt.Fprintf(&b, "   why: %s\n", s.Reason)
		}
		fmt.Fprintf(&b, "   %s\n", s.URL)
	}
	return b.String(), nil
}
