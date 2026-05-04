# Darek — YouTube transcript tool (design)

**Date:** 2026-05-04
**Status:** approved (awaiting implementation plan)
**Author:** brainstormed with Claude

## 1. Goal

Add a `youtube.transcript` agent tool that returns the transcript of a YouTube video as plain text plus a small header (title, channel, duration). The agent uses it for ad-hoc reasoning ("summarize this video", "what did they say about X") and as content the agent can pass to other tools (e.g. `links.save` notes, `memory.save`). The tool itself is stateless: it does not persist anything.

## 2. Scope

### In

- New `tools/youtube/` package containing:
  - A `Client` that fetches the YouTube watch page, parses `ytInitialPlayerResponse`, picks a caption track, and fetches the transcript.
  - A `Transcript` type implementing `tools.Tool` (`Name`/`Description`/`JSONSchema`/`Execute`).
- Tool name: `youtube.transcript`.
- Args: `url` (required, string) and `lang` (optional, ISO code).
- Output: a single string with a 3-line header and a blank line followed by the plain transcript text.
- Always-on registration in `cmd/darek` — no config gate. Stdlib-only.

### Out (deferred)

- Videos without captions (would require audio download + Whisper). Returns `"no captions available"` for v1.
- Timestamped output. Plain text only.
- Persisting transcripts to Postgres or to `links.summary`. Tool is stateless; the agent decides what to do with the result.
- Direct CLI subcommand (`darek youtube transcript <url>`). Agent-only for v1.
- Live streams, age-restricted videos, region-locked videos. These surface the underlying error verbatim.

## 3. Tool surface

### Schema

```json
{
  "type": "object",
  "properties": {
    "url":  { "type": "string", "description": "YouTube video URL (watch?v=, youtu.be/, shorts/, embed/)." },
    "lang": { "type": "string", "description": "Optional ISO language code (e.g. 'en', 'es'). Defaults to manual English, then auto English, then first available track." }
  },
  "required": ["url"],
  "additionalProperties": false
}
```

### Output format

```
Title: <video title>
Channel: <channel name>
Duration: <Mm Ss>

<transcript text — newlines collapsed to spaces, sentences flow as one paragraph>
```

If the transcript is longer than the registry's 20 000-char cap, the registry truncates and appends its standard truncation marker; the tool itself does not truncate.

### Errors (returned to the agent verbatim)

- `invalid YouTube URL: <input>` — could not parse a video ID.
- `video not accessible (private, removed, or region-locked)` — watch page returned 200 but `ytInitialPlayerResponse` was missing or had no `videoDetails`.
- `no captions available` — `captionTracks` array missing or empty.
- `language %q not available; have: en, es, de` — explicit `lang` arg did not match any track.
- Network/HTTP errors are wrapped with the URL: `fetch watch page: <err>`, `fetch transcript: <err>`.

## 4. `tools/youtube/` package

### `client.go`

```go
package youtube

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "regexp"
    "time"
)

type Client struct {
    http *http.Client
    base string // override for tests; defaults to "https://www.youtube.com"
}

func NewClient(httpClient *http.Client) *Client {
    if httpClient == nil { httpClient = http.DefaultClient }
    return &Client{http: httpClient, base: "https://www.youtube.com"}
}

type Result struct {
    Title    string
    Channel  string
    Duration time.Duration
    Text     string // plain transcript, no timestamps
}

// ExtractVideoID parses any supported YouTube URL form. Exposed for testing.
func ExtractVideoID(rawURL string) (string, error)

// Fetch returns the transcript for a video. lang is optional ("" = default fallback).
func (c *Client) Fetch(ctx context.Context, rawURL, lang string) (Result, error)
```

`Fetch` flow:

1. `ExtractVideoID(rawURL)` — regex-based; supports `watch?v=ID`, `youtu.be/ID`, `shorts/ID`, `embed/ID`, with arbitrary query strings.
2. `GET <base>/watch?v=<id>` with `User-Agent: Mozilla/5.0 (Macintosh; …) Chrome/120` and `Accept-Language: en-US,en;q=0.9`. Without these headers YouTube serves the EU consent page instead of the video page.
3. Pull `ytInitialPlayerResponse = {…};` from the HTML using a non-greedy regex bounded by `</script>`.
4. `json.Unmarshal` into a struct with `videoDetails {title, author, lengthSeconds}` and `captions.playerCaptionsTracklistRenderer.captionTracks[]` (each track has `baseUrl`, `languageCode`, `kind`, `name.simpleText`).
5. Pick a track:
   - If the `captionTracks` list is empty → return `"no captions available"`. (Checked before language matching, so a `lang` arg against a video with no captions still surfaces the right error.)
   - If `lang != ""`: first track whose `languageCode == lang`. None → return the "have: …" error listing available codes.
   - Otherwise: first non-`asr` (manual) track with `languageCode == "en"`; else first `asr` track with `languageCode == "en"`; else first track in the list.
6. `GET <track.BaseUrl>&fmt=json3` (the URL already has its own query string, append `&fmt=json3`).
7. Walk the JSON3 `events[]`. For each event: concatenate `seg.utf8` for every `seg` in `segs`. Skip events without `segs`. Replace `\n` with space, collapse runs of whitespace to a single space, trim.
8. Return `Result{Title, Channel, Duration, Text}`.

### `tools.go`

```go
package youtube

import (
    "context"
    "encoding/json"
    "fmt"

    "darek/tools"
)

const transcriptSchema = `{ ... }` // schema from §3

type Transcript struct {
    client *Client
}

func NewTranscript(client *Client) *Transcript { return &Transcript{client: client} }

func (Transcript) Name() string             { return "youtube.transcript" }
func (Transcript) Description() string      { return "Fetch the transcript of a YouTube video as plain text." }
func (Transcript) JSONSchema() json.RawMessage { return json.RawMessage(transcriptSchema) }

func (t *Transcript) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    var in struct {
        URL  string `json:"url"`
        Lang string `json:"lang,omitempty"`
    }
    if err := json.Unmarshal(args, &in); err != nil {
        return "", fmt.Errorf("invalid args: %w", err)
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

`formatDuration` renders `lengthSeconds` as:
- `"42s"` for <1m
- `"7m 13s"` for <1h
- `"1h 42m 09s"` for ≥1h

Tiny helper, ~10 lines.

The package compiles against stdlib only. `tools.Tool` is satisfied via `*Transcript`.

## 5. Wiring in `cmd/darek`

`cmd/darek/chat.go` builds the registry (around line 75 in the current code) and then registers tools — first the always-on ones (`memory.RecallTool`, `memory.SaveTool`), then config-gated ones inside `if cfg.X != nil` blocks.

Add the YouTube tool to the always-on group, immediately after the memory tools:

```go
ytClient := youtube.NewClient(&http.Client{Timeout: 15 * time.Second})
if err := reg.Register(youtube.NewTranscript(ytClient)); err != nil {
    return fmt.Errorf("register youtube.transcript: %w", err)
}
```

No config gate — the tool needs no credentials and has no opt-out toggle in v1. If we later want to disable it (e.g. for a privacy mode), gating goes through a `tools.youtube.enabled` config flag; out of scope now.

The 15-second timeout covers both HTTP requests inside `Fetch` (watch page + transcript). The registry's own per-tool timeout (`cfg.Agent.ToolTimeout`) is the outer bound.

## 6. Testing

### `tools/youtube/client_test.go`

- **`TestExtractVideoID`** — table:
  - `https://www.youtube.com/watch?v=dQw4w9WgXcQ` → `dQw4w9WgXcQ`
  - `https://youtu.be/dQw4w9WgXcQ` → same
  - `https://youtu.be/dQw4w9WgXcQ?t=42` → same
  - `https://www.youtube.com/shorts/abcDEF12345` → `abcDEF12345`
  - `https://www.youtube.com/embed/abcDEF12345?rel=0` → `abcDEF12345`
  - `https://www.youtube.com/watch?v=ID&list=PL...` → `ID`
  - `not a url`, `https://example.com/watch?v=ID`, `""` → error.

- **`TestPickTrack`** — table over `(tracks, lang) → expected pick or error`:
  - Empty tracks → `"no captions available"`.
  - `[en-asr, en]`, lang="" → picks `en` (manual preferred).
  - `[en-asr]`, lang="" → picks `en-asr`.
  - `[fr]`, lang="" → picks `fr` (only one available).
  - `[en, es]`, lang="es" → picks `es`.
  - `[en]`, lang="fr" → error `"language \"fr\" not available; have: en"`.

- **`TestParsePlayerResponse`** — feed in a fixture HTML containing `ytInitialPlayerResponse = {…};` with known title/author/lengthSeconds/captionTracks. Assert parsed values.

- **`TestParseJSON3`** — feed a fixture with multiple events, some with `segs`, some without. Assert collapsed text.

- **`TestFetch_HappyPath`** — `httptest.Server` serves fixture watch HTML at `/watch` and fixture JSON3 at `/api/timedtext`. Client points `base` at the test server. Assert returned `Result` matches expected.

- **`TestFetch_NoCaptions`** — fixture watch page with empty `captionTracks` → `"no captions available"`.

- **`TestFetch_VideoNotAccessible`** — fixture watch page missing `ytInitialPlayerResponse` (e.g. private-video page) → `"video not accessible..."`.

- **`TestFetch_LanguageNotAvailable`** — request `lang="fr"` against a fixture with only `en` → expected error string.

### `tools/youtube/tools_test.go`

- **`TestTranscript_NameDescriptionSchema`** — sanity: name is `youtube.transcript`, description is non-empty, schema is valid JSON with `url` required.
- **`TestTranscript_Execute`** — wires `NewTranscript` over a fake `Client` (interface-extracted in tests if needed, or using a stub server). Asserts the formatted output matches `Title: …\nChannel: …\nDuration: …\n\n<text>`.
- **`TestTranscript_Execute_BadArgs`** — empty `url` → error.

### Fixtures

`tools/youtube/testdata/`:
- `watch_with_captions.html` — minimal HTML, ~50 lines, containing the `ytInitialPlayerResponse` script block with two caption tracks.
- `watch_no_captions.html` — same shape but `captionTracks: []`.
- `watch_private.html` — page without the `ytInitialPlayerResponse` block.
- `transcript.json3` — small JSON3 with ~5 events.

No live YouTube calls in CI. A real-network smoke test could go behind the `integration` build tag (matching `make test-integration`) but is out of scope for v1.

## 7. Observability

Tool execution is already wrapped by `tools.Registry.Execute`:
- Span `tool.execute` with `tool.name="youtube.transcript"`, args/result chars.
- `darek.tool.calls` counter (`outcome`), `darek.tool.latency` histogram.

No new instruments for v1. The two HTTP requests (watch page, transcript) are inside the tool span; if we later want per-dependency metrics, wrap them in `obs.Dep("youtube_watch", …)` and `obs.Dep("youtube_transcript", …)`. Out of scope now.

## 8. Risks

- **Page-structure brittleness.** Google can rename or restructure `ytInitialPlayerResponse` or the `captionTracks` JSON. The shape has been stable for years, but a breakage would affect every call. Mitigation: clear error messages so the failure mode is "tool errors out" not "tool returns garbage", and the test fixtures make a fix straightforward.
- **Consent / region pages.** EU IPs can hit a consent interstitial. Setting a US-flavored `Accept-Language` and a Chrome UA dodges this in practice. If it becomes a problem, accept a cookie like `CONSENT=YES+1`.
- **Rate limiting.** Heavy use from one IP can get throttled. For a personal-assistant CLI this is unlikely; if it happens, the error surfaces to the agent.
- **Truncation.** A 90-minute podcast transcript exceeds 20 KB and gets truncated by the registry. The agent sees the truncation marker and can adjust (e.g. ask the user for a section, or summarize the visible portion). Acceptable for v1.
- **`youtube.com` TOS.** Scraping the watch page for personal use is in a gray zone; same posture as `yt-dlp`. Acceptable for a single-user CLI.

## 9. Out of scope

Recapped from §2:

- Audio download + Whisper for videos without captions.
- Timestamped output.
- Persisting transcripts.
- Direct CLI subcommand.
- Per-dep observability.
- Privacy/disable toggle.
