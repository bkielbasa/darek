package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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

// videoIDRe matches an 11-char YouTube ID: letters, digits, '-', '_'.
var videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

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
