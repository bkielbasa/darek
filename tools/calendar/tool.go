package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ListEventsTool struct {
	Sources *Sources
}

func (ListEventsTool) Name() string { return "calendar.list_events" }
func (lt ListEventsTool) Description() string {
	return "List calendar events between two timestamps. Empty calendar → all configured sources. Times in RFC3339."
}
func (lt ListEventsTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"from":{"type":"string","description":"RFC3339 inclusive lower bound; defaults to now"},
			"to":{"type":"string","description":"RFC3339 exclusive upper bound; defaults to from+24h"},
			"calendar":{"type":"string","description":"calendar nickname, omit for all"}
		},
		"required":[]
	}`)
}

func (lt ListEventsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		From     string `json:"from"`
		To       string `json:"to"`
		Calendar string `json:"calendar"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	from := time.Now()
	if p.From != "" {
		t, err := time.Parse(time.RFC3339, p.From)
		if err != nil {
			return "", fmt.Errorf("from: %w", err)
		}
		from = t
	}
	to := from.Add(24 * time.Hour)
	if p.To != "" {
		t, err := time.Parse(time.RFC3339, p.To)
		if err != nil {
			return "", fmt.Errorf("to: %w", err)
		}
		to = t
	}
	events, err := lt.Sources.ListEvents(ctx, from, to, p.Calendar)
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return "no events in window", nil
	}
	var b strings.Builder
	for _, ev := range events {
		fmt.Fprintf(&b, "[%s] %s — %s",
			ev.Calendar,
			ev.Start.Format(time.RFC3339),
			ev.Summary)
		if ev.Location != "" {
			fmt.Fprintf(&b, " @ %s", ev.Location)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}
