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

type UpdateEventTool struct {
	Sources *Sources
}

func (UpdateEventTool) Name() string { return "calendar.update_event" }
func (UpdateEventTool) Description() string {
	return "Update a calendar event by UID. PATCH semantics: only fields present in the call are changed."
}
func (UpdateEventTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"calendar":{"type":"string"},
			"uid":{"type":"string","description":"from list_events"},
			"summary":{"type":"string"},
			"start":{"type":"string"},
			"end":{"type":"string"},
			"all_day":{"type":"boolean"},
			"description":{"type":"string"},
			"location":{"type":"string"},
			"attendees":{"type":"array","items":{"type":"string"},"description":"replaces the full attendee list"},
			"send_invites":{"type":"boolean","default":false}
		},
		"required":["calendar","uid"]
	}`)
}

func (t UpdateEventTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var raw map[string]json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	calendar, err := requireString(raw, "calendar")
	if err != nil {
		return "", err
	}
	uid, err := requireString(raw, "uid")
	if err != nil {
		return "", err
	}
	if calendar == "" {
		return "", fmt.Errorf("calendar: required")
	}
	if uid == "" {
		return "", fmt.Errorf("uid: required")
	}

	allDay := false
	if v, ok := raw["all_day"]; ok {
		if err := json.Unmarshal(v, &allDay); err != nil {
			return "", fmt.Errorf("all_day: %w", err)
		}
	}

	patch := EventPatch{}
	patchFields := 0
	if v, ok := raw["summary"]; ok {
		var sv string
		if err := json.Unmarshal(v, &sv); err != nil {
			return "", fmt.Errorf("summary: %w", err)
		}
		patch.Summary = &sv
		patchFields++
	}
	if v, ok := raw["description"]; ok {
		var sv string
		if err := json.Unmarshal(v, &sv); err != nil {
			return "", fmt.Errorf("description: %w", err)
		}
		patch.Description = &sv
		patchFields++
	}
	if v, ok := raw["location"]; ok {
		var sv string
		if err := json.Unmarshal(v, &sv); err != nil {
			return "", fmt.Errorf("location: %w", err)
		}
		patch.Location = &sv
		patchFields++
	}
	if v, ok := raw["start"]; ok {
		var sv string
		if err := json.Unmarshal(v, &sv); err != nil {
			return "", fmt.Errorf("start: %w", err)
		}
		ts, err := parseEventTime(sv, allDay, "start")
		if err != nil {
			return "", err
		}
		patch.Start = &ts
		patchFields++
	}
	if v, ok := raw["end"]; ok {
		var sv string
		if err := json.Unmarshal(v, &sv); err != nil {
			return "", fmt.Errorf("end: %w", err)
		}
		ts, err := parseEventTime(sv, allDay, "end")
		if err != nil {
			return "", err
		}
		patch.End = &ts
		patchFields++
	}
	if _, ok := raw["all_day"]; ok {
		patch.AllDay = &allDay
		patchFields++
	}
	if v, ok := raw["attendees"]; ok {
		var atts []string
		if err := json.Unmarshal(v, &atts); err != nil {
			return "", fmt.Errorf("attendees: %w", err)
		}
		patch.Attendees = &atts
		patchFields++
	}
	if v, ok := raw["send_invites"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			return "", fmt.Errorf("send_invites: %w", err)
		}
		patch.SendInvites = b
		// notification flag, not a "patch field" — don't increment patchFields
	}

	if patchFields == 0 {
		return "", fmt.Errorf("no fields to update")
	}
	if patch.Start != nil && patch.End != nil && patch.End.Before(*patch.Start) {
		return "", fmt.Errorf("end must not be before start")
	}

	ev, err := t.Sources.Update(ctx, calendar, uid, patch)
	if err != nil {
		return "", err
	}
	return formatCreatedOrUpdated(ev), nil
}

// requireString decodes raw[key] into a string. Returns "" if absent.
// Returns an error only if the key is present but not a JSON string.
func requireString(raw map[string]json.RawMessage, key string) (string, error) {
	v, ok := raw[key]
	if !ok {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", fmt.Errorf("%s: expected string, got invalid JSON: %w", key, err)
	}
	return s, nil
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

type DeleteEventTool struct {
	Sources *Sources
}

func (DeleteEventTool) Name() string { return "calendar.delete_event" }
func (DeleteEventTool) Description() string {
	return "Delete a calendar event by UID."
}
func (DeleteEventTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"calendar":{"type":"string"},
			"uid":{"type":"string"},
			"send_invites":{"type":"boolean","default":false}
		},
		"required":["calendar","uid"]
	}`)
}

func (t DeleteEventTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Calendar    string `json:"calendar"`
		UID         string `json:"uid"`
		SendInvites bool   `json:"send_invites"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	if p.Calendar == "" {
		return "", fmt.Errorf("calendar: required")
	}
	if p.UID == "" {
		return "", fmt.Errorf("uid: required")
	}
	if err := t.Sources.Delete(ctx, p.Calendar, p.UID, p.SendInvites); err != nil {
		return "", err
	}
	return fmt.Sprintf("deleted: %s from %s", p.UID, p.Calendar), nil
}
