package google

import (
	"context"
	"fmt"
	"time"

	"darek/tools/calendar"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	calsvc "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

const Scope = calsvc.CalendarReadonlyScope

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
	res, err := call.Do()
	if err != nil {
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
