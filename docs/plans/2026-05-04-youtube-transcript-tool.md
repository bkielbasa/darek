# YouTube Transcript Tool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `youtube.transcript` agent tool that fetches a YouTube video's transcript by scraping the watch page for caption tracks. Returns plain text plus a small header (title, channel, duration).

**Architecture:** New stdlib-only package `tools/youtube/`. A `Client` extracts the video ID, fetches the watch page, parses `ytInitialPlayerResponse`, picks a caption track (manual-en → auto-en → first; or honors an explicit `lang`), fetches the JSON3 transcript, and assembles plain text. A `Transcript` type wraps the client and implements `tools.Tool`. Registered always-on in `cmd/darek/chat.go` next to the memory tools.

**Tech Stack:** Go stdlib only (`net/http`, `encoding/json`, `regexp`, `net/url`, `strings`, `time`, `context`). Tests use `github.com/stretchr/testify/require` and `httptest`.

**Design source:** [docs/specs/2026-05-04-youtube-transcript-tool-design.md](../specs/2026-05-04-youtube-transcript-tool-design.md), approved 2026-05-04.

**Out of scope (deferred):** Whisper fallback for caption-less videos, timestamped output, persistence, direct CLI subcommand, per-dependency observability, privacy/disable toggle.

---

## File Map

| Path | Responsibility |
|---|---|
| `tools/youtube/client.go` | (create) `Client`, `Result`, `ExtractVideoID`, `Fetch`, internal helpers (`pickTrack`, `parsePlayerResponse`, `parseJSON3`, `formatDuration`). |
| `tools/youtube/client_test.go` | (create) unit tests for ID extraction, player-response parsing, track selection, JSON3 parsing, duration formatting, end-to-end `Fetch` via httptest. |
| `tools/youtube/tools.go` | (create) `Transcript` type implementing `tools.Tool` (`Name`/`Description`/`JSONSchema`/`Execute`) plus `formatResult`. |
| `tools/youtube/tools_test.go` | (create) tool-interface checks, happy-path `Execute` via httptest, bad-args path. |
| `tools/youtube/testdata/watch_with_captions.html` | (create) minimal fixture watch page with `ytInitialPlayerResponse` containing two caption tracks. |
| `tools/youtube/testdata/watch_no_captions.html` | (create) fixture with empty `captionTracks`. |
| `tools/youtube/testdata/watch_private.html` | (create) fixture without `ytInitialPlayerResponse`. |
| `tools/youtube/testdata/transcript.json3` | (create) small JSON3 fixture (~5 events). |
| `cmd/darek/chat.go` | (modify) register `youtube.NewTranscript` after `memory.SaveTool`. |
| `README.md` | (modify) add `youtube.transcript` to the "Agent tools" section. |

---

## Task 1 — Package skeleton: types and `ExtractVideoID` (TDD)

**Files:**
- Create: `tools/youtube/client.go`
- Create: `tools/youtube/client_test.go`

The video-ID extractor is pure logic — perfect first slice. After this task the package compiles and has its first passing test.

- [ ] **Step 1: Create the package with bare types and a stub `ExtractVideoID`**

Create `tools/youtube/client.go`:

```go
package youtube

import (
	"net/http"
	"regexp"
	"time"
)

type Client struct {
	http *http.Client
	base string
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{http: httpClient, base: "https://www.youtube.com"}
}

type Result struct {
	Title    string
	Channel  string
	Duration time.Duration
	Text     string
}

// ExtractVideoID parses any supported YouTube URL form and returns the 11-char video ID.
func ExtractVideoID(rawURL string) (string, error) {
	return "", nil // stub — Step 3 fills this in
}

// videoIDRe matches an 11-char YouTube ID: letters, digits, '-', '_'.
var videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
```

- [ ] **Step 2: Write the failing test**

Create `tools/youtube/client_test.go`:

```go
package youtube

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractVideoID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		err  bool
	}{
		{"watch", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"watch with extra params", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PLxxx&t=42", "dQw4w9WgXcQ", false},
		{"youtu.be", "https://youtu.be/dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"youtu.be with timestamp", "https://youtu.be/dQw4w9WgXcQ?t=42", "dQw4w9WgXcQ", false},
		{"shorts", "https://www.youtube.com/shorts/abcDEF12345", "abcDEF12345", false},
		{"embed", "https://www.youtube.com/embed/abcDEF12345?rel=0", "abcDEF12345", false},
		{"http scheme", "http://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"no scheme", "youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"not youtube", "https://example.com/watch?v=dQw4w9WgXcQ", "", true},
		{"garbage", "not a url", "", true},
		{"empty", "", "", true},
		{"watch missing v", "https://www.youtube.com/watch?list=PLxxx", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractVideoID(tc.in)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
```

- [ ] **Step 3: Run the test, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/youtube/ -run TestExtractVideoID -v`
Expected: failures — most cases return empty string instead of expected ID.

- [ ] **Step 4: Implement `ExtractVideoID`**

Replace the stub in `tools/youtube/client.go`:

```go
func ExtractVideoID(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("invalid YouTube URL: %q", rawURL)
	}
	// Tolerate scheme-less input by adding one.
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid YouTube URL: %q", rawURL)
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	switch host {
	case "youtu.be":
		id := strings.TrimPrefix(u.Path, "/")
		if videoIDRe.MatchString(id) {
			return id, nil
		}
	case "youtube.com", "m.youtube.com", "music.youtube.com":
		switch {
		case u.Path == "/watch":
			id := u.Query().Get("v")
			if videoIDRe.MatchString(id) {
				return id, nil
			}
		case strings.HasPrefix(u.Path, "/shorts/"):
			id := strings.TrimPrefix(u.Path, "/shorts/")
			id = strings.SplitN(id, "/", 2)[0]
			if videoIDRe.MatchString(id) {
				return id, nil
			}
		case strings.HasPrefix(u.Path, "/embed/"):
			id := strings.TrimPrefix(u.Path, "/embed/")
			id = strings.SplitN(id, "/", 2)[0]
			if videoIDRe.MatchString(id) {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("invalid YouTube URL: %q", rawURL)
}
```

Add to imports:

```go
import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)
```

- [ ] **Step 5: Run the test, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/youtube/ -run TestExtractVideoID -v`
Expected: PASS for all subtests.

- [ ] **Step 6: Commit**

```bash
git add tools/youtube/client.go tools/youtube/client_test.go
git commit -m "feat(tools/youtube): video ID extraction"
```

---

## Task 2 — `parsePlayerResponse` (TDD)

**Files:**
- Modify: `tools/youtube/client.go`
- Modify: `tools/youtube/client_test.go`
- Create: `tools/youtube/testdata/watch_with_captions.html`
- Create: `tools/youtube/testdata/watch_private.html`

The watch page contains a JSON blob assigned to `ytInitialPlayerResponse`. We extract it, decode the fields we need, and return a parsed struct. Done as a private helper for testability.

- [ ] **Step 1: Add the `playerResponse` types to `client.go`**

Add to `tools/youtube/client.go`:

```go
type playerResponse struct {
	VideoDetails struct {
		Title         string `json:"title"`
		Author        string `json:"author"`
		LengthSeconds string `json:"lengthSeconds"`
	} `json:"videoDetails"`
	Captions struct {
		Tracklist struct {
			CaptionTracks []captionTrack `json:"captionTracks"`
		} `json:"playerCaptionsTracklistRenderer"`
	} `json:"captions"`
}

type captionTrack struct {
	BaseURL      string `json:"baseUrl"`
	LanguageCode string `json:"languageCode"`
	Kind         string `json:"kind"` // "asr" for auto-generated, empty for manual
	Name         struct {
		SimpleText string `json:"simpleText"`
	} `json:"name"`
}

// playerResponseRe captures `ytInitialPlayerResponse = { ... };` from HTML.
// Non-greedy on `.+?` and anchored to the trailing `};` then a closing script tag
// or newline boundary so we stop at the right brace.
var playerResponseRe = regexp.MustCompile(`(?s)ytInitialPlayerResponse\s*=\s*(\{.+?\})\s*;\s*(?:var|</script>|\n)`)

// parsePlayerResponse extracts and decodes the player-response JSON from a watch-page HTML body.
// Returns an error with the literal text "video not accessible (private, removed, or region-locked)"
// when the script tag is missing or the JSON has no videoDetails.
func parsePlayerResponse(html string) (*playerResponse, error) {
	m := playerResponseRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return nil, fmt.Errorf("video not accessible (private, removed, or region-locked)")
	}
	var pr playerResponse
	if err := json.Unmarshal([]byte(m[1]), &pr); err != nil {
		return nil, fmt.Errorf("parse player response: %w", err)
	}
	if pr.VideoDetails.Title == "" {
		return nil, fmt.Errorf("video not accessible (private, removed, or region-locked)")
	}
	return &pr, nil
}
```

Add `"encoding/json"` to imports if not already present.

- [ ] **Step 2: Create fixture `watch_with_captions.html`**

Create `tools/youtube/testdata/watch_with_captions.html`:

```html
<!doctype html>
<html>
<head><title>fixture</title></head>
<body>
<script>
var someOtherVar = 1;
ytInitialPlayerResponse = {"videoDetails":{"title":"Test Video","author":"Test Channel","lengthSeconds":"433"},"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[{"baseUrl":"https://example.invalid/timedtext?v=ID&lang=en","languageCode":"en","name":{"simpleText":"English"}},{"baseUrl":"https://example.invalid/timedtext?v=ID&lang=en&kind=asr","languageCode":"en","kind":"asr","name":{"simpleText":"English (auto-generated)"}}]}}};
var ytInitialData = {};
</script>
</body>
</html>
```

- [ ] **Step 3: Create fixture `watch_private.html`**

Create `tools/youtube/testdata/watch_private.html`:

```html
<!doctype html>
<html>
<head><title>YouTube</title></head>
<body>
<script>
// no ytInitialPlayerResponse assignment
var ytInitialData = {};
</script>
</body>
</html>
```

- [ ] **Step 4: Write the failing tests**

Append to `tools/youtube/client_test.go`:

```go
import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)
```

(Merge `os` into the existing import block — keep one import block at the top of the file.)

```go
func TestParsePlayerResponse_Happy(t *testing.T) {
	b, err := os.ReadFile("testdata/watch_with_captions.html")
	require.NoError(t, err)

	pr, err := parsePlayerResponse(string(b))
	require.NoError(t, err)
	require.Equal(t, "Test Video", pr.VideoDetails.Title)
	require.Equal(t, "Test Channel", pr.VideoDetails.Author)
	require.Equal(t, "433", pr.VideoDetails.LengthSeconds)
	require.Len(t, pr.Captions.Tracklist.CaptionTracks, 2)
	require.Equal(t, "en", pr.Captions.Tracklist.CaptionTracks[0].LanguageCode)
	require.Equal(t, "", pr.Captions.Tracklist.CaptionTracks[0].Kind)
	require.Equal(t, "asr", pr.Captions.Tracklist.CaptionTracks[1].Kind)
}

func TestParsePlayerResponse_Private(t *testing.T) {
	b, err := os.ReadFile("testdata/watch_private.html")
	require.NoError(t, err)

	_, err = parsePlayerResponse(string(b))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not accessible")
}
```

- [ ] **Step 5: Run the tests, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/youtube/ -run TestParsePlayerResponse -v`
Expected: PASS.

If the regex fails to match the fixture, widen the trailing boundary (e.g. add `};` plain-end alternative) until the test passes — the goal is to capture exactly the JSON object.

- [ ] **Step 6: Commit**

```bash
git add tools/youtube/client.go tools/youtube/client_test.go tools/youtube/testdata/
git commit -m "feat(tools/youtube): parse ytInitialPlayerResponse from watch page"
```

---

## Task 3 — `pickTrack` (TDD)

**Files:**
- Modify: `tools/youtube/client.go`
- Modify: `tools/youtube/client_test.go`

Track selection is pure logic: given a slice of tracks and an optional language, return the chosen track or an error.

- [ ] **Step 1: Write the failing test**

Append to `tools/youtube/client_test.go`:

```go
func TestPickTrack(t *testing.T) {
	en := captionTrack{LanguageCode: "en"}
	enAuto := captionTrack{LanguageCode: "en", Kind: "asr"}
	es := captionTrack{LanguageCode: "es"}
	fr := captionTrack{LanguageCode: "fr"}

	cases := []struct {
		name    string
		tracks  []captionTrack
		lang    string
		want    captionTrack
		wantErr string // substring match
	}{
		{"empty", nil, "", captionTrack{}, "no captions available"},
		{"empty with lang", nil, "en", captionTrack{}, "no captions available"},
		{"manual en preferred over auto", []captionTrack{enAuto, en}, "", en, ""},
		{"only auto en", []captionTrack{enAuto}, "", enAuto, ""},
		{"first when no en", []captionTrack{fr, es}, "", fr, ""},
		{"explicit es", []captionTrack{en, es}, "es", es, ""},
		{"explicit en exact", []captionTrack{enAuto, en}, "en", enAuto, ""}, // first match wins on explicit
		{"explicit missing", []captionTrack{en}, "fr", captionTrack{}, `language "fr" not available; have: en`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickTrack(tc.tracks, tc.lang)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
```

Note: when `lang="en"` is explicit and both `en` and `en-asr` exist, return the first match in iteration order. The default-fallback path (manual-then-auto) only applies when `lang` is empty.

- [ ] **Step 2: Run the test, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/youtube/ -run TestPickTrack -v`
Expected: FAIL — `pickTrack` doesn't exist.

- [ ] **Step 3: Implement `pickTrack`**

Add to `tools/youtube/client.go`:

```go
func pickTrack(tracks []captionTrack, lang string) (captionTrack, error) {
	if len(tracks) == 0 {
		return captionTrack{}, fmt.Errorf("no captions available")
	}
	if lang != "" {
		for _, t := range tracks {
			if t.LanguageCode == lang {
				return t, nil
			}
		}
		var have []string
		seen := map[string]bool{}
		for _, t := range tracks {
			if !seen[t.LanguageCode] {
				seen[t.LanguageCode] = true
				have = append(have, t.LanguageCode)
			}
		}
		return captionTrack{}, fmt.Errorf("language %q not available; have: %s", lang, strings.Join(have, ", "))
	}
	// Default fallback: manual en, then auto en, then first track.
	for _, t := range tracks {
		if t.LanguageCode == "en" && t.Kind == "" {
			return t, nil
		}
	}
	for _, t := range tracks {
		if t.LanguageCode == "en" && t.Kind == "asr" {
			return t, nil
		}
	}
	return tracks[0], nil
}
```

- [ ] **Step 4: Run the test, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/youtube/ -run TestPickTrack -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/youtube/client.go tools/youtube/client_test.go
git commit -m "feat(tools/youtube): caption track selection"
```

---

## Task 4 — `parseJSON3` and `formatDuration` (TDD)

**Files:**
- Modify: `tools/youtube/client.go`
- Modify: `tools/youtube/client_test.go`
- Create: `tools/youtube/testdata/transcript.json3`

Two small pure helpers:
- `parseJSON3` walks the JSON3 events and returns concatenated, whitespace-collapsed text.
- `formatDuration` renders `time.Duration` in the spec's three forms.

- [ ] **Step 1: Create fixture `transcript.json3`**

Create `tools/youtube/testdata/transcript.json3`:

```json
{
  "events": [
    {"tStartMs": 0, "dDurationMs": 2000, "segs": [{"utf8": "Hello,"}, {"utf8": " world."}]},
    {"tStartMs": 2000, "dDurationMs": 1000},
    {"tStartMs": 3000, "dDurationMs": 2500, "segs": [{"utf8": "This is\na test."}]},
    {"tStartMs": 5500, "dDurationMs": 1500, "segs": [{"utf8": "  Multiple   spaces.  "}]},
    {"tStartMs": 7000, "dDurationMs": 1000, "segs": [{"utf8": "\n"}]}
  ]
}
```

Expected collapsed text: `"Hello, world. This is a test. Multiple spaces."` (newline event with only whitespace contributes nothing visible after collapse).

- [ ] **Step 2: Write the failing tests**

Append to `tools/youtube/client_test.go`:

```go
func TestParseJSON3(t *testing.T) {
	b, err := os.ReadFile("testdata/transcript.json3")
	require.NoError(t, err)

	got, err := parseJSON3(b)
	require.NoError(t, err)
	require.Equal(t, "Hello, world. This is a test. Multiple spaces.", got)
}

func TestParseJSON3_Empty(t *testing.T) {
	got, err := parseJSON3([]byte(`{"events":[]}`))
	require.NoError(t, err)
	require.Equal(t, "", got)
}

func TestParseJSON3_Bad(t *testing.T) {
	_, err := parseJSON3([]byte(`{not json`))
	require.Error(t, err)
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{42 * time.Second, "42s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 00s"},
		{7*time.Minute + 13*time.Second, "7m 13s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{1 * time.Hour, "1h 00m 00s"},
		{1*time.Hour + 42*time.Minute + 9*time.Second, "1h 42m 09s"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			require.Equal(t, tc.want, formatDuration(tc.in))
		})
	}
}
```

Add `"time"` to the test file's imports if not already present (it is, from the import block at the top).

- [ ] **Step 3: Run the tests, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/youtube/ -run "TestParseJSON3|TestFormatDuration" -v`
Expected: FAIL — symbols don't exist.

- [ ] **Step 4: Implement `parseJSON3` and `formatDuration`**

Add to `tools/youtube/client.go`:

```go
type json3Doc struct {
	Events []struct {
		Segs []struct {
			Utf8 string `json:"utf8"`
		} `json:"segs"`
	} `json:"events"`
}

var wsRe = regexp.MustCompile(`\s+`)

// parseJSON3 walks the JSON3 events and returns the concatenated transcript text
// with all whitespace runs (including newlines) collapsed to single spaces and trimmed.
// A single space is inserted before each event past the first so consecutive events
// like "Hello, world." and "This is a test." don't run together as "world.This is".
// The collapse pass washes out any resulting double-spaces at empty-event boundaries.
func parseJSON3(b []byte) (string, error) {
	var d json3Doc
	if err := json.Unmarshal(b, &d); err != nil {
		return "", fmt.Errorf("parse json3: %w", err)
	}
	var sb strings.Builder
	for i, ev := range d.Events {
		if i > 0 {
			sb.WriteByte(' ')
		}
		for _, s := range ev.Segs {
			sb.WriteString(s.Utf8)
		}
	}
	collapsed := wsRe.ReplaceAllString(sb.String(), " ")
	return strings.TrimSpace(collapsed), nil
}

// formatDuration renders d as "42s" (<1m), "7m 13s" (<1h), or "1h 42m 09s" (>=1h).
func formatDuration(d time.Duration) string {
	total := int(d / time.Second)
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	if total < 3600 {
		return fmt.Sprintf("%dm %02ds", total/60, total%60)
	}
	return fmt.Sprintf("%dh %02dm %02ds", total/3600, (total%3600)/60, total%60)
}
```

- [ ] **Step 5: Run the tests, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/youtube/ -run "TestParseJSON3|TestFormatDuration" -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add tools/youtube/client.go tools/youtube/client_test.go tools/youtube/testdata/transcript.json3
git commit -m "feat(tools/youtube): JSON3 parser and duration formatter"
```

---

## Task 5 — `Fetch` end-to-end via httptest (TDD)

**Files:**
- Modify: `tools/youtube/client.go`
- Modify: `tools/youtube/client_test.go`
- Create: `tools/youtube/testdata/watch_no_captions.html`

This task wires all the helpers together. The test uses `httptest.Server` and points the client's `base` field at the test server. The fixture `baseUrl` for caption tracks must point at the same test server too.

- [ ] **Step 1: Create fixture `watch_no_captions.html`**

Create `tools/youtube/testdata/watch_no_captions.html`:

```html
<!doctype html>
<html>
<head><title>fixture</title></head>
<body>
<script>
ytInitialPlayerResponse = {"videoDetails":{"title":"Silent Video","author":"Quiet Channel","lengthSeconds":"30"},"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[]}}};
</script>
</body>
</html>
```

- [ ] **Step 2: Write the failing tests**

Append to `tools/youtube/client_test.go`:

```go
import (
	"net/http"
	"net/http/httptest"
	"strings"
)
```

(Merge into the existing import block.)

```go
// newFakeYouTube serves /watch from a fixture HTML, rewriting embedded
// caption baseUrl hosts to point at the test server, and serves /timedtext
// from the JSON3 fixture. Returns the server (caller must Close).
//
// The fixtures hard-code https://example.invalid as the caption host. This
// helper substitutes the running httptest server's URL at request time, so
// the watch-page response points the client back at the same fake server
// for the transcript fetch.
func newFakeYouTube(t *testing.T, watchFixture string) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch":
			b, err := os.ReadFile("testdata/" + watchFixture)
			require.NoError(t, err)
			body := strings.ReplaceAll(string(b), "https://example.invalid", srv.URL)
			_, _ = w.Write([]byte(body))
		case "/timedtext":
			require.Equal(t, "json3", r.URL.Query().Get("fmt"))
			b, err := os.ReadFile("testdata/transcript.json3")
			require.NoError(t, err)
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	})
	srv.Start()
	return srv
}

func TestFetch_HappyPath(t *testing.T) {
	srv := newFakeYouTube(t, "watch_with_captions.html")
	defer srv.Close()

	c := NewClient(srv.Client())
	c.base = srv.URL

	res, err := c.Fetch(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "")
	require.NoError(t, err)
	require.Equal(t, "Test Video", res.Title)
	require.Equal(t, "Test Channel", res.Channel)
	require.Equal(t, 433*time.Second, res.Duration)
	require.Equal(t, "Hello, world. This is a test. Multiple spaces.", res.Text)
}

func TestFetch_NoCaptions(t *testing.T) {
	srv := newFakeYouTube(t, "watch_no_captions.html")
	defer srv.Close()

	c := NewClient(srv.Client())
	c.base = srv.URL

	_, err := c.Fetch(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no captions available")
}

func TestFetch_VideoNotAccessible(t *testing.T) {
	srv := newFakeYouTube(t, "watch_private.html")
	defer srv.Close()

	c := NewClient(srv.Client())
	c.base = srv.URL

	_, err := c.Fetch(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not accessible")
}

func TestFetch_LanguageNotAvailable(t *testing.T) {
	srv := newFakeYouTube(t, "watch_with_captions.html")
	defer srv.Close()

	c := NewClient(srv.Client())
	c.base = srv.URL

	_, err := c.Fetch(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "fr")
	require.Error(t, err)
	require.Contains(t, err.Error(), `language "fr" not available`)
}

func TestFetch_BadURL(t *testing.T) {
	c := NewClient(nil)
	_, err := c.Fetch(context.Background(), "https://example.com/foo", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid YouTube URL")
}
```

Add `"context"` to the test file imports if needed.

- [ ] **Step 3: Run the tests, expect failure**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/youtube/ -run TestFetch -v`
Expected: FAIL — `Fetch` is not implemented.

- [ ] **Step 4: Implement `Fetch`**

Add to `tools/youtube/client.go`:

```go
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Fetch returns the transcript Result for a video. lang is optional ("" = default fallback).
func (c *Client) Fetch(ctx context.Context, rawURL, lang string) (Result, error) {
	id, err := ExtractVideoID(rawURL)
	if err != nil {
		return Result{}, err
	}
	html, err := c.getString(ctx, c.base+"/watch?v="+id)
	if err != nil {
		return Result{}, fmt.Errorf("fetch watch page: %w", err)
	}
	pr, err := parsePlayerResponse(html)
	if err != nil {
		return Result{}, err
	}
	track, err := pickTrack(pr.Captions.Tracklist.CaptionTracks, lang)
	if err != nil {
		return Result{}, err
	}
	transURL := track.BaseURL
	if strings.Contains(transURL, "?") {
		transURL += "&fmt=json3"
	} else {
		transURL += "?fmt=json3"
	}
	body, err := c.getBytes(ctx, transURL)
	if err != nil {
		return Result{}, fmt.Errorf("fetch transcript: %w", err)
	}
	text, err := parseJSON3(body)
	if err != nil {
		return Result{}, err
	}

	secs, _ := strconv.Atoi(pr.VideoDetails.LengthSeconds)
	return Result{
		Title:    pr.VideoDetails.Title,
		Channel:  pr.VideoDetails.Author,
		Duration: time.Duration(secs) * time.Second,
		Text:     text,
	}, nil
}

func (c *Client) getString(ctx context.Context, url string) (string, error) {
	b, err := c.getBytes(ctx, url)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (c *Client) getBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
```

Add `"context"`, `"io"`, `"strconv"` to the imports.

- [ ] **Step 5: Run the tests, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/youtube/ -run TestFetch -v`
Expected: PASS for all five subtests.

- [ ] **Step 6: Run the whole package**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/youtube/ -v`
Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add tools/youtube/client.go tools/youtube/client_test.go tools/youtube/testdata/watch_no_captions.html
git commit -m "feat(tools/youtube): end-to-end Fetch with httptest coverage"
```

---

## Task 6 — `Transcript` tool wrapper (TDD)

**Files:**
- Create: `tools/youtube/tools.go`
- Create: `tools/youtube/tools_test.go`

The Tool implementation. Thin layer over `Client.Fetch` plus output formatting.

- [ ] **Step 1: Create the tool file with stubs**

Create `tools/youtube/tools.go`:

```go
package youtube

import (
	"context"
	"encoding/json"
	"fmt"

	"darek/tools"
)

// transcriptSchema is the JSON-schema describing youtube.transcript args.
const transcriptSchema = `{
	"type":"object",
	"properties":{
		"url":{"type":"string","description":"YouTube video URL (watch?v=, youtu.be/, shorts/, embed/)."},
		"lang":{"type":"string","description":"Optional ISO language code (e.g. 'en', 'es'). Defaults to manual English, then auto English, then first available track."}
	},
	"required":["url"],
	"additionalProperties":false
}`

// fetcher is the subset of *Client used by Transcript; lets tests inject a fake.
type fetcher interface {
	Fetch(ctx context.Context, rawURL, lang string) (Result, error)
}

type Transcript struct {
	client fetcher
}

func NewTranscript(client *Client) *Transcript {
	return &Transcript{client: client}
}

// Compile-time check.
var _ tools.Tool = (*Transcript)(nil)

func (*Transcript) Name() string { return "youtube.transcript" }

func (*Transcript) Description() string {
	return "Fetch the transcript of a YouTube video as plain text. Returns title, channel, duration, and transcript text."
}

func (*Transcript) JSONSchema() json.RawMessage {
	return json.RawMessage(transcriptSchema)
}

func (t *Transcript) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL  string `json:"url"`
		Lang string `json:"lang,omitempty"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	if in.URL == "" {
		return "", fmt.Errorf("url is required")
	}
	res, err := t.client.Fetch(ctx, in.URL, in.Lang)
	if err != nil {
		return "", err
	}
	return formatResult(res), nil
}

func formatResult(r Result) string {
	return fmt.Sprintf("Title: %s\nChannel: %s\nDuration: %s\n\n%s",
		r.Title, r.Channel, formatDuration(r.Duration), r.Text)
}
```

- [ ] **Step 2: Write the tests**

Create `tools/youtube/tools_test.go`:

```go
package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeFetcher struct {
	res     Result
	err     error
	gotURL  string
	gotLang string
}

func (f *fakeFetcher) Fetch(ctx context.Context, rawURL, lang string) (Result, error) {
	f.gotURL = rawURL
	f.gotLang = lang
	return f.res, f.err
}

func TestTranscript_NameDescriptionSchema(t *testing.T) {
	tr := &Transcript{}
	require.Equal(t, "youtube.transcript", tr.Name())
	require.NotEmpty(t, tr.Description())

	var schema map[string]any
	require.NoError(t, json.Unmarshal(tr.JSONSchema(), &schema))
	required, _ := schema["required"].([]any)
	require.Contains(t, required, "url")
}

func TestTranscript_Execute_Happy(t *testing.T) {
	f := &fakeFetcher{res: Result{
		Title:    "T",
		Channel:  "C",
		Duration: 73 * time.Second,
		Text:     "the body",
	}}
	tr := &Transcript{client: f}

	out, err := tr.Execute(context.Background(), json.RawMessage(`{"url":"https://youtu.be/abcDEF12345","lang":"es"}`))
	require.NoError(t, err)
	require.Equal(t, "https://youtu.be/abcDEF12345", f.gotURL)
	require.Equal(t, "es", f.gotLang)
	require.Equal(t, "Title: T\nChannel: C\nDuration: 1m 13s\n\nthe body", out)
}

func TestTranscript_Execute_MissingURL(t *testing.T) {
	tr := &Transcript{client: &fakeFetcher{}}
	_, err := tr.Execute(context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "url is required")
}

func TestTranscript_Execute_BadJSON(t *testing.T) {
	tr := &Transcript{client: &fakeFetcher{}}
	_, err := tr.Execute(context.Background(), json.RawMessage(`{not json`))
	require.Error(t, err)
}

func TestTranscript_Execute_FetchError(t *testing.T) {
	f := &fakeFetcher{err: errors.New("no captions available")}
	tr := &Transcript{client: f}
	_, err := tr.Execute(context.Background(), json.RawMessage(`{"url":"https://youtu.be/abcDEF12345"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "no captions available")
}
```

- [ ] **Step 3: Run the tests, expect pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./tools/youtube/ -v`
Expected: PASS (everything in the package).

- [ ] **Step 4: Commit**

```bash
git add tools/youtube/tools.go tools/youtube/tools_test.go
git commit -m "feat(tools/youtube): Transcript tool implementing tools.Tool"
```

---

## Task 7 — Wire into `cmd/darek/chat.go`

**Files:**
- Modify: `cmd/darek/chat.go`

Register the tool always-on, alongside the memory tools.

- [ ] **Step 1: Add the import**

In `cmd/darek/chat.go`, add to the existing import block (the `darek/tools/...` alphabetical group):

```go
"darek/tools/youtube"
```

(Existing imports include `darek/tools/calendar`, `darek/tools/freshrss`, etc. — slot in alphabetically.)

Also add `"net/http"` and `"time"` if they are not already imported in this file.

- [ ] **Step 2: Register the tool**

Locate the registration of `memory.SaveTool` (around line 83). Immediately after that block, add:

```go
ytClient := youtube.NewClient(&http.Client{Timeout: 15 * time.Second})
if err := reg.Register(youtube.NewTranscript(ytClient)); err != nil {
	return err
}
```

- [ ] **Step 3: Build**

Run: `cd /Users/bklimczak/Projects/darek && go build ./...`
Expected: clean build.

- [ ] **Step 4: Run tests**

Run: `cd /Users/bklimczak/Projects/darek && go test ./...`
Expected: all tests pass. (The tool registry's startup tests in `tools/registry_test.go` may already exercise registration; nothing new should break.)

- [ ] **Step 5: Smoke-check tool listing**

Run: `cd /Users/bklimczak/Projects/darek && go build -o /tmp/darek ./cmd/darek && /tmp/darek doctor 2>&1 | head -40 || true`
Expected: doctor runs (it requires Postgres + OpenAI configured to fully succeed; failing on those is fine — we're checking the binary builds and doesn't panic at registration). If doctor prints registered tools, confirm `youtube.transcript` is listed; otherwise rely on the unit tests already verifying the registration path.

- [ ] **Step 6: Commit**

```bash
git add cmd/darek/chat.go
git commit -m "feat(cmd/darek): register youtube.transcript tool"
```

---

## Task 8 — Update `README.md`

**Files:**
- Modify: `README.md`

Add a "YouTube" entry to the "Agent tools" section so `youtube.transcript` is discoverable.

- [ ] **Step 1: Add the README entry**

In `README.md`, find the "Agent tools" section (currently ends after `**Mail**: ...`). After the Mail bullet, append:

```markdown

**YouTube**
- `youtube.transcript(url, lang?)` — fetch a YouTube video's transcript as plain text. `lang` is optional (e.g. `"es"`); default prefers manual English, then auto-generated English, then the first available track. Returns title, channel, duration, then the transcript. Errors with `"no captions available"` when the video has no captions, or `"video not accessible..."` for private/removed/region-locked videos.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs(readme): document youtube.transcript tool"
```

---

## Task 9 — Final verification

**Files:** none (no edits)

- [ ] **Step 1: Full unit-test run**

Run: `cd /Users/bklimczak/Projects/darek && make test`
Expected: all tests pass.

- [ ] **Step 2: Lint**

Run: `cd /Users/bklimczak/Projects/darek && make lint`
Expected: no `go vet` warnings.

- [ ] **Step 3: Manual end-to-end (optional, requires real network + agent config)**

If you have `~/.darek/config.yaml` and OpenAI configured, run:

```bash
./darek "fetch the transcript of https://www.youtube.com/watch?v=dQw4w9WgXcQ and tell me the first sentence"
```

Expected: agent calls `youtube.transcript`, returns a result, and answers from the transcript. If captions are unavailable or YouTube has changed page structure, the error surfaces verbatim — at which point the regex in `parsePlayerResponse` likely needs widening (re-run Task 2 tests against a freshly captured fixture).
