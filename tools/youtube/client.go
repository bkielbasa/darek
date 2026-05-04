package youtube

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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
