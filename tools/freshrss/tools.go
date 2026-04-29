package freshrss

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// API is the subset of *Client the tools need; lets tests inject a fake.
type API interface {
	List(ctx context.Context, opts ListOpts) ([]Article, error)
	Get(ctx context.Context, id string) (*Article, error)
	Mark(ctx context.Context, id string, act Action) error
}

type ListTool struct{ Client API }

func (ListTool) Name() string { return "freshrss.list_articles" }
func (ListTool) Description() string {
	return "List articles from FreshRSS. filter: 'unread' (default), 'starred', 'all'. Optional feed_url to scope to one feed."
}
func (ListTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"filter":{"type":"string","enum":["unread","starred","all"],"default":"unread"},
			"feed_url":{"type":"string","description":"scope to a specific feed URL"},
			"limit":{"type":"integer","minimum":1,"maximum":200,"default":50}
		},
		"required":[]
	}`)
}
func (lt ListTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Filter  string `json:"filter"`
		FeedURL string `json:"feed_url"`
		Limit   int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	opts := ListOpts{FeedURL: p.FeedURL, Limit: p.Limit}
	switch p.Filter {
	case "", "unread":
		opts.Filter = FilterUnread
	case "starred":
		opts.Filter = FilterStarred
	case "all":
		opts.Filter = FilterAll
	default:
		return "", fmt.Errorf("invalid filter %q", p.Filter)
	}
	arts, err := lt.Client.List(ctx, opts)
	if err != nil {
		return "", err
	}
	if len(arts) == 0 {
		return "no matching articles", nil
	}
	var b strings.Builder
	for _, a := range arts {
		marks := ""
		if a.Starred {
			marks += "★"
		}
		if a.Read {
			marks += "✓"
		}
		fmt.Fprintf(&b, "[%s]%s %s — %s\n  feed: %s | %s\n",
			a.ID, marks, a.Title, a.URL, a.Feed, a.Published.Format(time.RFC3339))
	}
	return b.String(), nil
}

type GetTool struct{ Client API }

func (GetTool) Name() string        { return "freshrss.get_article" }
func (GetTool) Description() string { return "Fetch the full content of one FreshRSS article by id." }
func (GetTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{"id":{"type":"string"}},
		"required":["id"]
	}`)
}
func (gt GetTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct{ ID string `json:"id"` }
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if p.ID == "" {
		return "", fmt.Errorf("id required")
	}
	a, err := gt.Client.Get(ctx, p.ID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\nFeed: %s\nURL: %s\nPublished: %s\n\n%s\n",
		a.Title, a.Feed, a.URL, a.Published.Format(time.RFC3339), a.Summary)
	return b.String(), nil
}

type MarkTool struct{ Client API }

func (MarkTool) Name() string { return "freshrss.mark" }
func (MarkTool) Description() string {
	return "Mark a FreshRSS article: action ∈ {read, unread, star, unstar}."
}
func (MarkTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"id":{"type":"string"},
			"action":{"type":"string","enum":["read","unread","star","unstar"]}
		},
		"required":["id","action"]
	}`)
}
func (mt MarkTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if p.ID == "" {
		return "", fmt.Errorf("id required")
	}
	var act Action
	switch p.Action {
	case "read":
		act = ActionMarkRead
	case "unread":
		act = ActionMarkUnread
	case "star":
		act = ActionStar
	case "unstar":
		act = ActionUnstar
	default:
		return "", fmt.Errorf("invalid action %q", p.Action)
	}
	if err := mt.Client.Mark(ctx, p.ID, act); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: %s", p.Action, p.ID), nil
}
