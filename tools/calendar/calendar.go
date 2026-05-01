package calendar

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Event struct {
	Calendar    string    // nickname of the source it came from
	UID         string
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	AllDay      bool
}

var ErrReadOnly = errors.New("calendar is read-only")

// NewEvent is the input shape for creating a calendar event.
type NewEvent struct {
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	AllDay      bool
	Attendees   []string // emails
	SendInvites bool
}

// EventPatch carries PATCH-style updates: only non-nil pointer fields are applied.
// For Attendees: nil means "no change", a non-nil pointer to a slice (including empty)
// replaces the full attendee list.
type EventPatch struct {
	Summary     *string
	Description *string
	Location    *string
	Start       *time.Time
	End         *time.Time
	AllDay      *bool
	Attendees   *[]string
	SendInvites bool
}

// WritableCalendarSource is implemented by sources that support mutations.
// Read-only sources (e.g. iCal feeds) don't implement it.
type WritableCalendarSource interface {
	CalendarSource
	CreateEvent(ctx context.Context, in NewEvent) (Event, error)
	UpdateEvent(ctx context.Context, uid string, patch EventPatch) (Event, error)
	DeleteEvent(ctx context.Context, uid string, sendInvites bool) error
}

type CalendarSource interface {
	Nickname() string
	ListEvents(ctx context.Context, from, to time.Time) ([]Event, error)
}

type Sources struct {
	mu   sync.RWMutex
	bynm map[string]CalendarSource
}

func NewSources() *Sources { return &Sources{bynm: map[string]CalendarSource{}} }

func (s *Sources) Add(src CalendarSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := src.Nickname()
	if n == "" {
		return fmt.Errorf("calendar source has empty nickname")
	}
	if _, ok := s.bynm[n]; ok {
		return fmt.Errorf("calendar source %q already registered", n)
	}
	s.bynm[n] = src
	return nil
}

func (s *Sources) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.bynm))
	for n := range s.bynm {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ListEvents fans out across one or all sources and concatenates results,
// then sorts ascending by start time.
func (s *Sources) ListEvents(ctx context.Context, from, to time.Time, calendar string) ([]Event, error) {
	s.mu.RLock()
	var targets []CalendarSource
	if calendar == "" {
		for _, src := range s.bynm {
			targets = append(targets, src)
		}
	} else {
		src, ok := s.bynm[calendar]
		if !ok {
			s.mu.RUnlock()
			return nil, fmt.Errorf("unknown calendar %q (have: %v)", calendar, s.namesUnlocked())
		}
		targets = []CalendarSource{src}
	}
	s.mu.RUnlock()

	var (
		out   []Event
		errs  []string
	)
	for _, src := range targets {
		ev, err := src.ListEvents(ctx, from, to)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", src.Nickname(), err))
			continue
		}
		out = append(out, ev...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	if len(errs) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("all calendar sources failed: %v", errs)
	}
	return out, nil
}

func (s *Sources) namesUnlocked() []string {
	out := make([]string, 0, len(s.bynm))
	for n := range s.bynm {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
