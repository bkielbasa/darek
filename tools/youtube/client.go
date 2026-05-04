package youtube

import (
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
