package links

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type SaveTool struct{ Store *Store }

func (SaveTool) Name() string { return "links.save" }
func (SaveTool) Description() string {
	return `Save (or update) a link with the user's rating, tags, and notes. ` +
		`Saving the same URL twice updates the existing entry — by default tags are merged. ` +
		`Use this whenever the user shares a link they want remembered, or rates one you found.`
}
func (SaveTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"url":{"type":"string"},
			"title":{"type":"string"},
			"rating":{"type":"integer","minimum":1,"maximum":5,"description":"1=disliked, 5=loved; omit for unrated"},
			"tags":{"type":"array","items":{"type":"string"},"description":"lowercased on save"},
			"notes":{"type":"string","description":"why the user liked/disliked it; this is the main signal for similarity later"},
			"replace_tags":{"type":"boolean","description":"true to overwrite existing tags instead of merging"},
			"source":{"type":"string","description":"defaults to user; set to e.g. freshrss when importing"}
		},
		"required":["url"]
	}`)
}
func (st SaveTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		URL         string   `json:"url"`
		Title       string   `json:"title"`
		Rating      *int     `json:"rating"`
		Tags        []string `json:"tags"`
		Notes       string   `json:"notes"`
		ReplaceTags bool     `json:"replace_tags"`
		Source      string   `json:"source"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	id, err := st.Store.Save(ctx, SaveInput{
		URL: p.URL, Title: p.Title, Rating: p.Rating, Tags: p.Tags,
		Notes: p.Notes, ReplaceTags: p.ReplaceTags, Source: p.Source,
	})
	if err != nil {
		return "", err
	}
	rating := "unrated"
	if p.Rating != nil {
		rating = fmt.Sprintf("rating=%d", *p.Rating)
	}
	return fmt.Sprintf("saved link %s (%s)", id, rating), nil
}

type SearchTool struct{ Store *Store }

func (SearchTool) Name() string { return "links.search" }
func (SearchTool) Description() string {
	return "Search saved links by query string and/or filters (min_rating 1..5, tags, source, since). Returns id, url, rating, tags, snippet."
}
func (SearchTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"query":{"type":"string"},
			"min_rating":{"type":"integer","minimum":1,"maximum":5},
			"tags":{"type":"array","items":{"type":"string"}},
			"source":{"type":"string"},
			"since":{"type":"string","description":"RFC3339 lower bound on created_at"},
			"limit":{"type":"integer","minimum":1,"maximum":100,"default":20}
		},
		"required":[]
	}`)
}
func (st SearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query     string   `json:"query"`
		MinRating int      `json:"min_rating"`
		Tags      []string `json:"tags"`
		Source    string   `json:"source"`
		Since     string   `json:"since"`
		Limit     int      `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	opts := SearchOpts{Query: p.Query, MinRating: p.MinRating, Tags: p.Tags, Source: p.Source, Limit: p.Limit}
	if p.Since != "" {
		t, err := time.Parse(time.RFC3339, p.Since)
		if err != nil {
			return "", fmt.Errorf("since: %w", err)
		}
		opts.Since = t
	}
	got, err := st.Store.Search(ctx, opts)
	if err != nil {
		return "", err
	}
	return formatLinks(got), nil
}

type SimilarTool struct{ Store *Store }

func (SimilarTool) Name() string { return "links.similar" }
func (SimilarTool) Description() string {
	return "Find the user's previously-rated links most similar to a given text (e.g., the title+summary of a candidate article). " +
		"Use this to predict whether the user would like a new piece of content — review the returned ratings and notes, then reason."
}
func (SimilarTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"text":{"type":"string","description":"free text to compare against — title+summary works well"},
			"limit":{"type":"integer","minimum":1,"maximum":50,"default":10}
		},
		"required":["text"]
	}`)
}
func (st SimilarTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Text  string `json:"text"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	got, err := st.Store.Similar(ctx, p.Text, p.Limit)
	if err != nil {
		return "", err
	}
	if len(got) == 0 {
		return "no similar rated links", nil
	}
	return formatLinks(got), nil
}

func formatLinks(ls []Link) string {
	if len(ls) == 0 {
		return "no matching links"
	}
	var b strings.Builder
	for _, l := range ls {
		rating := "—"
		if l.Rating != nil {
			rating = fmt.Sprintf("%d/5", *l.Rating)
		}
		title := l.Title
		fmt.Fprintf(&b, "[%s] %s %s\n  %s",
			l.ID, rating, l.URL, title)
		if len(l.Tags) > 0 {
			fmt.Fprintf(&b, "\n  tags: %s", strings.Join(l.Tags, ", "))
		}
		if l.Notes != "" {
			fmt.Fprintf(&b, "\n  notes: %s", l.Notes)
		}
		b.WriteString("\n")
	}
	return b.String()
}
