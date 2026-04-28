# Calendars Implementation Plan

> **For agentic workers:** Use `superpowers:subagent-driven-development` to implement this plan task-by-task.

**Goal:** Add read-only calendar support (Google Calendar OAuth + iCal HTTP feeds) behind a pluggable `CalendarSource` interface, with a `calendar.list_events` tool the agent can call.

**Architecture:** New `tools/calendar/` package. `CalendarSource` interface, two implementations (`google`, `ical`). A `Source` registry that maps nickname → source. The tool takes `from`, `to`, optional `calendar` (nickname). Google OAuth tokens stored on disk under `~/.darek/oauth/` (per-account JSON file). A new `darek calendar refresh-token <nickname>` subcommand runs the interactive consent flow.

**Tech Stack:** `golang.org/x/oauth2`, `google.golang.org/api/calendar/v3` (Google Calendar), `github.com/arran4/golang-ical` (iCal parser).

**Out of scope:** CalDAV, Outlook (Microsoft Graph), calendar mutations.

---

## File Map

| Path | Responsibility |
|---|---|
| `tools/calendar/calendar.go` | `Event` struct, `CalendarSource` interface, `Sources` registry. |
| `tools/calendar/calendar_test.go` | Sources unit tests. |
| `tools/calendar/ical/ical.go` | HTTP iCal feed source. |
| `tools/calendar/ical/ical_test.go` | Tests with httptest server. |
| `tools/calendar/google/google.go` | Google Calendar source via OAuth. |
| `tools/calendar/google/oauth.go` | Token store + refresh helpers. |
| `tools/calendar/google/oauth_test.go` | Token-store unit tests (no real OAuth). |
| `tools/calendar/tool.go` | `ListEventsTool` implementing `tools.Tool`. |
| `tools/calendar/tool_test.go` | Tool tests using a fake source. |
| `cmd/darek/refresh_token.go` | `darek calendar refresh-token <nickname>` subcommand. |
| `cmd/darek/main.go` | Add dispatch for `calendar` subcommand. |
| `cmd/darek/chat.go` | Register `ListEventsTool` if calendars configured. |
| `config/types.go` | Add `Calendars []CalendarSrc` to `Config`. |

---

## Task 1 — Calendar interface + Event + Sources

**Files:** Create `tools/calendar/calendar.go`, `tools/calendar/calendar_test.go`.

- [ ] **Step 1: Write `tools/calendar/calendar.go`**

```go
package calendar

import (
	"context"
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
```

- [ ] **Step 2: Write `tools/calendar/calendar_test.go`**

```go
package calendar

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeSrc struct {
	name   string
	events []Event
	err    error
}

func (f fakeSrc) Nickname() string { return f.name }
func (f fakeSrc) ListEvents(_ context.Context, _, _ time.Time) ([]Event, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.events, nil
}

func TestSources_ListAll_Sorted(t *testing.T) {
	s := NewSources()
	t1 := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	require.NoError(t, s.Add(fakeSrc{name: "a", events: []Event{{UID: "x", Start: t2}}}))
	require.NoError(t, s.Add(fakeSrc{name: "b", events: []Event{{UID: "y", Start: t1}}}))
	got, err := s.ListEvents(context.Background(), t1, t2.Add(time.Hour), "")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "y", got[0].UID)
	require.Equal(t, "x", got[1].UID)
}

func TestSources_ListByCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "a", events: []Event{{UID: "x"}}}))
	require.NoError(t, s.Add(fakeSrc{name: "b", events: []Event{{UID: "y"}}}))
	got, err := s.ListEvents(context.Background(), time.Time{}, time.Time{}, "b")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "y", got[0].UID)
}

func TestSources_UnknownCalendar(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "a"}))
	_, err := s.ListEvents(context.Background(), time.Time{}, time.Time{}, "nope")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown calendar")
}

func TestSources_PartialFailureIgnored(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "ok", events: []Event{{UID: "x"}}}))
	require.NoError(t, s.Add(fakeSrc{name: "bad", err: errors.New("network")}))
	got, err := s.ListEvents(context.Background(), time.Time{}, time.Time{}, "")
	require.NoError(t, err) // not all failed
	require.Len(t, got, 1)
}

func TestSources_AllFailed_Errors(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "bad1", err: errors.New("x")}))
	require.NoError(t, s.Add(fakeSrc{name: "bad2", err: errors.New("y")}))
	_, err := s.ListEvents(context.Background(), time.Time{}, time.Time{}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "all calendar sources failed")
}

func TestSources_Names_Sorted(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "b"}))
	require.NoError(t, s.Add(fakeSrc{name: "a"}))
	require.Equal(t, []string{"a", "b"}, s.Names())
}
```

- [ ] **Step 3: Run + commit**

```bash
go test ./tools/calendar/...
git add tools/calendar/calendar.go tools/calendar/calendar_test.go
git commit -m "feat(calendar): CalendarSource interface and Sources registry"
```

---

## Task 2 — iCal feed source

**Files:** Create `tools/calendar/ical/ical.go`, `tools/calendar/ical/ical_test.go`.

- [ ] **Step 1: Write `tools/calendar/ical/ical.go`**

```go
package ical

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"darek/tools/calendar"

	ics "github.com/arran4/golang-ical"
)

type Source struct {
	nickname string
	url      string
	client   *http.Client
}

func New(nickname, url string) *Source {
	return &Source{nickname: nickname, url: url, client: &http.Client{Timeout: 30 * time.Second}}
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
```

- [ ] **Step 2: Write `tools/calendar/ical/ical_test.go`**

```go
package ical

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const sample = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:e1@test
SUMMARY:Standup
DESCRIPTION:Daily sync
LOCATION:Zoom
DTSTART:20260428T090000Z
DTEND:20260428T093000Z
END:VEVENT
BEGIN:VEVENT
UID:e2@test
SUMMARY:Anniversary
DTSTART;VALUE=DATE:20260501
DTEND;VALUE=DATE:20260502
END:VEVENT
END:VCALENDAR
`

func TestSource_ListEvents_FromHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = w.Write([]byte(sample))
	}))
	defer srv.Close()

	s := New("test", srv.URL)
	from := time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	got, err := s.ListEvents(context.Background(), from, to)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "Standup", got[0].Summary)
	require.False(t, got[0].AllDay)
	require.Equal(t, "Anniversary", got[1].Summary)
	require.True(t, got[1].AllDay)
}

func TestSource_NonOK_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	s := New("test", srv.URL)
	_, err := s.ListEvents(context.Background(), time.Time{}, time.Time{})
	require.Error(t, err)
}
```

- [ ] **Step 3: Add deps + run + commit**

```bash
go get github.com/arran4/golang-ical
go mod tidy
go test ./tools/calendar/...
git add tools/calendar/ical/ go.mod go.sum
git commit -m "feat(calendar): iCal HTTP feed source"
```

---

## Task 3 — Google Calendar source + token store

**Files:** Create `tools/calendar/google/google.go`, `tools/calendar/google/oauth.go`, `tools/calendar/google/oauth_test.go`.

- [ ] **Step 1: Write `tools/calendar/google/oauth.go`**

```go
package google

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/oauth2"
)

// TokenStore persists OAuth2 tokens to ~/.darek/oauth/<nickname>.json.
type TokenStore struct {
	dir string
}

func NewTokenStore(dir string) *TokenStore { return &TokenStore{dir: dir} }

func (s *TokenStore) path(nickname string) string {
	return filepath.Join(s.dir, nickname+".json")
}

func (s *TokenStore) Save(nickname string, tok *oauth2.Token) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", s.dir, err)
	}
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	if err := os.WriteFile(s.path(nickname), b, 0o600); err != nil {
		return fmt.Errorf("write token: %w", err)
	}
	return nil
}

func (s *TokenStore) Load(nickname string) (*oauth2.Token, error) {
	b, err := os.ReadFile(s.path(nickname))
	if err != nil {
		return nil, fmt.Errorf("read token %s: %w", nickname, err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, fmt.Errorf("unmarshal token: %w", err)
	}
	return &tok, nil
}
```

- [ ] **Step 2: Write `tools/calendar/google/oauth_test.go`**

```go
package google

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestTokenStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewTokenStore(filepath.Join(dir, "oauth"))
	tok := &oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour).Truncate(time.Second)}
	require.NoError(t, s.Save("personal", tok))
	got, err := s.Load("personal")
	require.NoError(t, err)
	require.Equal(t, "a", got.AccessToken)
	require.Equal(t, "r", got.RefreshToken)
}

func TestTokenStore_LoadMissing(t *testing.T) {
	s := NewTokenStore(t.TempDir())
	_, err := s.Load("nope")
	require.Error(t, err)
}
```

- [ ] **Step 3: Write `tools/calendar/google/google.go`**

```go
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
// The redirect URL must match what the user configured in their Google Cloud project.
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
```

- [ ] **Step 4: Add deps + run + commit**

```bash
go get golang.org/x/oauth2
go get golang.org/x/oauth2/google
go get google.golang.org/api/calendar/v3
go get google.golang.org/api/option
go mod tidy
go test ./tools/calendar/google/...
git add tools/calendar/google/ go.mod go.sum
git commit -m "feat(calendar): Google Calendar source via OAuth2 + token store"
```

---

## Task 4 — `darek calendar refresh-token` subcommand

**Files:** Create `cmd/darek/refresh_token.go`. Modify `cmd/darek/main.go` to add `calendar` dispatch.

- [ ] **Step 1: Write `cmd/darek/refresh_token.go`**

```go
package main

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"darek/config"
	"darek/tools/calendar/google"
)

// runCalendar dispatches `darek calendar <subcmd> <args...>`.
func runCalendar(ctx context.Context, cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: darek calendar refresh-token <nickname>")
	}
	switch args[0] {
	case "refresh-token":
		if len(args) < 2 {
			return fmt.Errorf("usage: darek calendar refresh-token <nickname>")
		}
		return runRefreshToken(ctx, cfgPath, args[1])
	default:
		return fmt.Errorf("unknown calendar subcommand %q (try: refresh-token)", args[0])
	}
}

func runRefreshToken(ctx context.Context, cfgPath, nickname string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	var src *config.CalendarSrc
	for i := range cfg.Calendars {
		c := &cfg.Calendars[i]
		if c.Nickname == nickname {
			src = c
			break
		}
	}
	if src == nil {
		return fmt.Errorf("no calendar with nickname %q in config", nickname)
	}
	if src.Kind != "google" {
		return fmt.Errorf("calendar %q is not a Google source (kind=%s)", nickname, src.Kind)
	}
	clientID, err := config.ResolveSecret("env:" + src.ClientIDEnv)
	if err != nil {
		return fmt.Errorf("client id: %w", err)
	}
	clientSecret, err := config.ResolveSecret("env:" + src.ClientSecretEnv)
	if err != nil {
		return fmt.Errorf("client secret: %w", err)
	}
	oauthCfg := google.Config(clientID, clientSecret)

	authURL := oauthCfg.AuthCodeURL("state-token")
	fmt.Fprintf(os.Stderr, "Open this URL in a browser, grant access, then paste the code shown:\n\n%s\n\n", authURL)
	fmt.Fprint(os.Stderr, "code: ")
	code, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read code: %w", err)
	}
	code = strings.TrimSpace(code)
	// In case the user pasted a URL with `code=...` instead of the bare code, extract it.
	if u, perr := url.Parse(code); perr == nil && u.Query().Get("code") != "" {
		code = u.Query().Get("code")
	}

	tok, err := oauthCfg.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("exchange code: %w", err)
	}
	home, _ := os.UserHomeDir()
	store := google.NewTokenStore(filepath.Join(home, ".darek", "oauth"))
	if err := store.Save(nickname, tok); err != nil {
		return err
	}
	fmt.Printf("token saved for calendar %q\n", nickname)
	return nil
}
```

- [ ] **Step 2: Modify `cmd/darek/main.go`** — add a `case "calendar"` dispatch:

Find the switch in `cmd/darek/main.go`. Add this case before the `default`:

```go
	case "calendar":
		return runCalendar(ctx, cfgPath, args)
```

The full switch becomes:

```go
	switch cmd {
	case "migrate":
		return runMigrate(ctx, cfgPath)
	case "doctor":
		return runDoctor(ctx, cfgPath)
	case "calendar":
		return runCalendar(ctx, cfgPath, args)
	case "", "chat":
		return runChat(ctx, cfgPath, strings.Join(args, " "))
	default:
		return fmt.Errorf("unknown subcommand %q (try: chat, migrate, doctor, calendar)", cmd)
	}
```

- [ ] **Step 3: Build + commit**

```bash
make build  # may fail until config.CalendarSrc exists — that's Task 6
```

If build fails, that's expected at this step — Task 6 adds the config type. Commit anyway with a flag:

```bash
git add cmd/darek/refresh_token.go cmd/darek/main.go
git commit -m "feat(cmd): darek calendar refresh-token subcommand"
```

**Note:** if you want to keep CI green at every commit, do Task 6 (config additions) before Task 4. The order doesn't otherwise matter; the plan separates them for review focus.

→ **Reorder note for the implementer:** do Task 6 (config additions) BEFORE Task 4 if you'd like the build to remain green. Both orders converge.

---

## Task 5 — `calendar.list_events` tool

**Files:** Create `tools/calendar/tool.go`, `tools/calendar/tool_test.go`.

- [ ] **Step 1: Write `tools/calendar/tool.go`**

```go
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
```

- [ ] **Step 2: Write `tools/calendar/tool_test.go`**

```go
package calendar

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestListEventsTool_DefaultsAndFormatting(t *testing.T) {
	s := NewSources()
	now := time.Now()
	require.NoError(t, s.Add(fakeSrc{
		name: "personal",
		events: []Event{
			{Calendar: "personal", Summary: "Meeting", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Location: "Office"},
			{Calendar: "personal", Summary: "Lunch", Start: now.Add(3 * time.Hour), End: now.Add(4 * time.Hour)},
		},
	}))
	out, err := ListEventsTool{Sources: s}.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Contains(t, out, "[personal]")
	require.Contains(t, out, "Meeting @ Office")
	require.Contains(t, out, "Lunch")
}

func TestListEventsTool_NoEvents(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "p"}))
	out, err := ListEventsTool{Sources: s}.Execute(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "no events in window", out)
}

func TestListEventsTool_BadFrom(t *testing.T) {
	s := NewSources()
	require.NoError(t, s.Add(fakeSrc{name: "p"}))
	_, err := ListEventsTool{Sources: s}.Execute(context.Background(), json.RawMessage(`{"from":"bogus"}`))
	require.Error(t, err)
}
```

- [ ] **Step 3: Run + commit**

```bash
go test ./tools/calendar/...
git add tools/calendar/tool.go tools/calendar/tool_test.go
git commit -m "feat(calendar): list_events tool"
```

---

## Task 6 — Config schema + chat wiring

**Files:** Modify `config/types.go`, `cmd/darek/chat.go`.

- [ ] **Step 1: Add `CalendarSrc` to `config/types.go`**

After the existing types in `config/types.go`, append:

```go
type CalendarSrc struct {
	Kind            string `yaml:"kind"`             // "google" | "ical"
	Nickname        string `yaml:"nickname"`
	URL             string `yaml:"url"`              // for ical
	CalendarID      string `yaml:"calendar_id"`      // for google, default "primary"
	ClientIDEnv     string `yaml:"client_id_env"`    // for google
	ClientSecretEnv string `yaml:"client_secret_env"`// for google
}
```

In the `Config` struct, add the field:

```go
type Config struct {
	OpenAI    OpenAI    `yaml:"openai"`
	Postgres  Postgres  `yaml:"postgres"`
	OTEL      OTEL      `yaml:"otel"`
	Agent     Agent     `yaml:"agent"`
	Memory    Memory    `yaml:"memory"`
	Calendars []CalendarSrc `yaml:"calendars"`
}
```

- [ ] **Step 2: Update `config/testdata/config.example.yaml`** — add at the bottom:

```yaml
calendars:
  - kind: ical
    nickname: family
    url: https://calendar.example.com/feed.ics
  # - kind: google
  #   nickname: personal
  #   calendar_id: primary
  #   client_id_env: DAREK_GCAL_CLIENT_ID
  #   client_secret_env: DAREK_GCAL_CLIENT_SECRET
```

- [ ] **Step 3: Wire calendars into `cmd/darek/chat.go`**

After the memory tool registration in `runChat`, add:

```go
	// Calendar sources
	if len(cfg.Calendars) > 0 {
		srcs := calendar.NewSources()
		home, _ := os.UserHomeDir()
		store := googlecal.NewTokenStore(filepath.Join(home, ".darek", "oauth"))
		for _, c := range cfg.Calendars {
			switch c.Kind {
			case "ical":
				if err := srcs.Add(ical.New(c.Nickname, c.URL)); err != nil {
					return fmt.Errorf("calendar %s: %w", c.Nickname, err)
				}
			case "google":
				cid, err := config.ResolveSecret("env:" + c.ClientIDEnv)
				if err != nil {
					logger.WarnContext(ctx, "skipping google calendar", "nickname", c.Nickname, "error", err.Error())
					continue
				}
				cs, err := config.ResolveSecret("env:" + c.ClientSecretEnv)
				if err != nil {
					logger.WarnContext(ctx, "skipping google calendar", "nickname", c.Nickname, "error", err.Error())
					continue
				}
				oauthCfg := googlecal.Config(cid, cs)
				if err := srcs.Add(googlecal.NewSource(c.Nickname, c.CalendarID, oauthCfg, store)); err != nil {
					return fmt.Errorf("calendar %s: %w", c.Nickname, err)
				}
			default:
				logger.WarnContext(ctx, "unknown calendar kind", "kind", c.Kind, "nickname", c.Nickname)
			}
		}
		if len(srcs.Names()) > 0 {
			if err := reg.Register(calendar.ListEventsTool{Sources: srcs}); err != nil {
				return err
			}
		}
	}
```

Add imports to `chat.go`:

```go
	"path/filepath"

	"darek/tools/calendar"
	"darek/tools/calendar/ical"
	googlecal "darek/tools/calendar/google"
```

- [ ] **Step 4: Run + commit**

```bash
make build
make test
git add config/types.go config/testdata/config.example.yaml cmd/darek/chat.go
git commit -m "feat(cmd,config): wire calendar sources into chat command"
```

---

## Task 7 — README + final test pass

**Files:** Modify `README.md`.

- [ ] **Step 1: Add a "Calendars" section** to the README, after the existing layout:

```markdown
## Calendars

Calendars are read-only. Add sources to `~/.darek/config.yaml`:

```yaml
calendars:
  - kind: ical
    nickname: family
    url: https://calendar.example.com/feed.ics
  - kind: google
    nickname: personal
    calendar_id: primary  # or a specific calendar id
    client_id_env: DAREK_GCAL_CLIENT_ID
    client_secret_env: DAREK_GCAL_CLIENT_SECRET
```

For Google calendars, run the OAuth flow once per nickname:

```bash
./darek calendar refresh-token personal
```

The CLI prints an auth URL; visit it, paste back the code, the token is saved to `~/.darek/oauth/<nickname>.json`.
```

- [ ] **Step 2: Update the "Layout" block** in the README — add `tools/calendar/` line.

- [ ] **Step 3: Run + commit**

```bash
make test
make build
git add README.md
git commit -m "docs: README calendars section"
```

---

## Acceptance criteria

1. `make test` and `make test-integration` pass.
2. `darek doctor` still passes (calendar checks are not added to doctor in this plan).
3. With an iCal feed configured, `darek "what's on my calendar tomorrow?"` returns a list of events in the window.
4. With a Google calendar configured and `darek calendar refresh-token <nickname>` run once, the same query works against Google.
5. Multiple calendar sources are merged and sorted by start time.
