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

// parseEventTime parses a "start" or "end" string given the all_day flag.
// On all_day=true, it accepts YYYY-MM-DD; otherwise RFC3339.
func parseEventTime(s string, allDay bool, field string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("%s: required", field)
	}
	if allDay {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s: all_day requires YYYY-MM-DD, got %q", field, s)
		}
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: requires RFC3339 datetime, got %q (use all_day=true for date-only)", field, s)
	}
	return t, nil
}

type CreateEventTool struct {
	Sources *Sources
}

func (CreateEventTool) Name() string { return "calendar.create_event" }
func (CreateEventTool) Description() string {
	return "Create a calendar event on a writable calendar. Returns the created event with its UID."
}
func (CreateEventTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"calendar":{"type":"string","description":"nickname of a writable calendar"},
			"summary":{"type":"string"},
			"start":{"type":"string","description":"RFC3339 datetime, or YYYY-MM-DD if all_day=true"},
			"end":{"type":"string","description":"same format; defaults to start+1h (timed) or start+1 day (all_day)"},
			"all_day":{"type":"boolean","default":false},
			"description":{"type":"string"},
			"location":{"type":"string"},
			"attendees":{"type":"array","items":{"type":"string"}},
			"send_invites":{"type":"boolean","default":false}
		},
		"required":["calendar","summary","start"]
	}`)
}

func (t CreateEventTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Calendar    string   `json:"calendar"`
		Summary     string   `json:"summary"`
		Start       string   `json:"start"`
		End         string   `json:"end"`
		AllDay      bool     `json:"all_day"`
		Description string   `json:"description"`
		Location    string   `json:"location"`
		Attendees   []string `json:"attendees"`
		SendInvites bool     `json:"send_invites"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	if p.Calendar == "" {
		return "", fmt.Errorf("calendar: required")
	}
	if p.Summary == "" {
		return "", fmt.Errorf("summary: required")
	}
	start, err := parseEventTime(p.Start, p.AllDay, "start")
	if err != nil {
		return "", err
	}
	var end time.Time
	if p.End == "" {
		if p.AllDay {
			end = start.Add(24 * time.Hour)
		} else {
			end = start.Add(time.Hour)
		}
	} else {
		end, err = parseEventTime(p.End, p.AllDay, "end")
		if err != nil {
			return "", err
		}
	}
	if end.Before(start) {
		return "", fmt.Errorf("end must not be before start")
	}
	in := NewEvent{
		Summary:     p.Summary,
		Description: p.Description,
		Location:    p.Location,
		Start:       start,
		End:         end,
		AllDay:      p.AllDay,
		Attendees:   p.Attendees,
		SendInvites: p.SendInvites,
	}
	ev, err := t.Sources.Create(ctx, p.Calendar, in)
	if err != nil {
		return "", err
	}
	return formatCreatedOrUpdated(ev), nil
}

// formatCreatedOrUpdated renders an event in the same shape ListEventsTool uses,
// followed by a "uid: ..." line so the agent can address it later.
func formatCreatedOrUpdated(ev Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s — %s",
		ev.Calendar, ev.Start.Format(time.RFC3339), ev.Summary)
	if ev.Location != "" {
		fmt.Fprintf(&b, " @ %s", ev.Location)
	}
	fmt.Fprintf(&b, "\nuid: %s", ev.UID)
	return b.String()
}
