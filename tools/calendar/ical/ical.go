package ical

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"darek/tools/calendar"

	ics "github.com/arran4/golang-ical"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type Source struct {
	nickname string
	url      string
	client   *http.Client
}

func New(nickname, url string) *Source {
	return &Source{
		nickname: nickname,
		url:      url,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

func (s *Source) Nickname() string { return s.nickname }

func (s *Source) ListEvents(ctx context.Context, from, to time.Time) ([]calendar.Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", s.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("fetch %s: status %d", s.url, resp.StatusCode)
	}
	cal, err := ics.ParseCalendar(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse ics: %w", err)
	}
	var out []calendar.Event
	for _, e := range cal.Events() {
		ev, ok := convert(s.nickname, e)
		if !ok {
			continue
		}
		// Filter to window.
		if !from.IsZero() && ev.End.Before(from) {
			continue
		}
		if !to.IsZero() && ev.Start.After(to) {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

func convert(nickname string, e *ics.VEvent) (calendar.Event, bool) {
	uid := propValue(e, ics.ComponentPropertyUniqueId)
	summary := propValue(e, ics.ComponentPropertySummary)
	desc := propValue(e, ics.ComponentPropertyDescription)
	loc := propValue(e, ics.ComponentPropertyLocation)
	start, err := e.GetStartAt()
	if err != nil {
		return calendar.Event{}, false
	}
	end, err := e.GetEndAt()
	if err != nil {
		// Some VEVENTs use DURATION instead of DTEND. Fall back to start + 0.
		end = start
	}
	allDay := false
	if dt := e.GetProperty(ics.ComponentPropertyDtStart); dt != nil {
		if v, ok := dt.ICalParameters["VALUE"]; ok {
			for _, vv := range v {
				if vv == "DATE" {
					allDay = true
				}
			}
		}
	}
	return calendar.Event{
		Calendar:    nickname,
		UID:         uid,
		Summary:     summary,
		Description: desc,
		Location:    loc,
		Start:       start,
		End:         end,
		AllDay:      allDay,
	}, true
}

func propValue(e *ics.VEvent, p ics.ComponentProperty) string {
	if v := e.GetProperty(p); v != nil {
		return v.Value
	}
	return ""
}
