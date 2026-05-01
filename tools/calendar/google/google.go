package google

import (
	"context"
	"fmt"
	"time"

	"darek/obs"
	"darek/tools/calendar"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	calsvc "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

const Scope = calsvc.CalendarEventsScope

// Config returns an oauth2.Config built from a Google "OAuth client" client_id+secret.
// We default to the OOB ("urn:ietf:wg:oauth:2.0:oob") flow for desktop CLI use.
func Config(clientID, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{Scope},
		RedirectURL:  "urn:ietf:wg:oauth:2.0:oob",
	}
}

type Source struct {
	nickname string
	cfg      *oauth2.Config
	store    *TokenStore
	calID    string // "primary" by default
}

func NewSource(nickname, calendarID string, cfg *oauth2.Config, store *TokenStore) *Source {
	if calendarID == "" {
		calendarID = "primary"
	}
	return &Source{nickname: nickname, cfg: cfg, store: store, calID: calendarID}
}

func (s *Source) Nickname() string { return s.nickname }

func (s *Source) ListEvents(ctx context.Context, from, to time.Time) ([]calendar.Event, error) {
	tok, err := s.store.Load(s.nickname)
	if err != nil {
		return nil, fmt.Errorf("load token: %w", err)
	}
	httpClient := s.cfg.Client(ctx, tok)
	// Wrap the OAuth2 transport so each Google API request is traced.
	httpClient.Transport = otelhttp.NewTransport(httpClient.Transport)
	svc, err := calsvc.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("calendar svc: %w", err)
	}
	call := svc.Events.List(s.calID).
		SingleEvents(true).
		OrderBy("startTime").
		Context(ctx)
	if !from.IsZero() {
		call = call.TimeMin(from.Format(time.RFC3339))
	}
	if !to.IsZero() {
		call = call.TimeMax(to.Format(time.RFC3339))
	}
	var res *calsvc.Events
	if err := obs.Dep(ctx, "google_calendar", "list_events", func(ctx context.Context) error {
		var err error
		res, err = call.Do()
		return err
	}); err != nil {
		return nil, fmt.Errorf("events.list: %w", err)
	}
	out := make([]calendar.Event, 0, len(res.Items))
	for _, it := range res.Items {
		ev, ok := convert(s.nickname, it)
		if !ok {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

func convert(nickname string, it *calsvc.Event) (calendar.Event, bool) {
	if it == nil {
		return calendar.Event{}, false
	}
	start, allDay := parseDT(it.Start)
	end, _ := parseDT(it.End)
	if start.IsZero() {
		return calendar.Event{}, false
	}
	return calendar.Event{
		Calendar:    nickname,
		UID:         it.Id,
		Summary:     it.Summary,
		Description: it.Description,
		Location:    it.Location,
		Start:       start,
		End:         end,
		AllDay:      allDay,
	}, true
}

func parseDT(t *calsvc.EventDateTime) (time.Time, bool) {
	if t == nil {
		return time.Time{}, false
	}
	if t.DateTime != "" {
		if v, err := time.Parse(time.RFC3339, t.DateTime); err == nil {
			return v, false
		}
	}
	if t.Date != "" {
		if v, err := time.Parse("2006-01-02", t.Date); err == nil {
			return v, true
		}
	}
	return time.Time{}, false
}

// buildAPIEvent converts a NewEvent into the Google Calendar API shape.
func buildAPIEvent(in calendar.NewEvent) *calsvc.Event {
	out := &calsvc.Event{
		Summary:     in.Summary,
		Description: in.Description,
		Location:    in.Location,
		Start:       eventDateTime(in.Start, in.AllDay),
		End:         eventDateTime(in.End, in.AllDay),
	}
	if len(in.Attendees) > 0 {
		out.Attendees = make([]*calsvc.EventAttendee, 0, len(in.Attendees))
		for _, e := range in.Attendees {
			out.Attendees = append(out.Attendees, &calsvc.EventAttendee{Email: e})
		}
	}
	return out
}

// buildAPIPatch converts an EventPatch into a partial Google Calendar API event,
// using ForceSendFields so that empty values (e.g. cleared description, empty
// attendee list) are actually sent rather than dropped by omitempty.
func buildAPIPatch(p calendar.EventPatch) *calsvc.Event {
	out := &calsvc.Event{}
	allDay := false
	if p.AllDay != nil {
		allDay = *p.AllDay
	}
	if p.Summary != nil {
		out.Summary = *p.Summary
		out.ForceSendFields = append(out.ForceSendFields, "Summary")
	}
	if p.Description != nil {
		out.Description = *p.Description
		out.ForceSendFields = append(out.ForceSendFields, "Description")
	}
	if p.Location != nil {
		out.Location = *p.Location
		out.ForceSendFields = append(out.ForceSendFields, "Location")
	}
	if p.Start != nil {
		out.Start = eventDateTime(*p.Start, allDay)
	}
	if p.End != nil {
		out.End = eventDateTime(*p.End, allDay)
	}
	if p.Attendees != nil {
		out.Attendees = make([]*calsvc.EventAttendee, 0, len(*p.Attendees))
		for _, e := range *p.Attendees {
			out.Attendees = append(out.Attendees, &calsvc.EventAttendee{Email: e})
		}
		out.ForceSendFields = append(out.ForceSendFields, "Attendees")
	}
	return out
}

// eventDateTime renders a time as the right Google API date-or-datetime shape.
func eventDateTime(t time.Time, allDay bool) *calsvc.EventDateTime {
	if allDay {
		return &calsvc.EventDateTime{Date: t.Format("2006-01-02")}
	}
	return &calsvc.EventDateTime{DateTime: t.Format(time.RFC3339)}
}

// sendUpdates maps the bool flag to Google's enum for the sendUpdates query param.
func sendUpdates(send bool) string {
	if send {
		return "all"
	}
	return "none"
}
