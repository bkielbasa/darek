package links

import (
	"net/url"
	"strings"
)

// Classify returns the kind label ("article" | "video" | "tweet" | "podcast" |
// "other") for a URL based on host and path heuristics. Patterns live here so
// adding hosts is a one-line change.
func Classify(raw string) string {
	if raw == "" {
		return "article"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "article"
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")

	// video
	switch host {
	case "youtube.com", "youtu.be", "vimeo.com", "tiktok.com":
		return "video"
	}

	// tweet / micropost
	switch host {
	case "twitter.com", "x.com", "bsky.app":
		return "tweet"
	}
	if strings.HasPrefix(host, "mastodon.") {
		return "tweet"
	}

	// podcast
	switch host {
	case "anchor.fm", "podcasts.apple.com", "overcast.fm":
		return "podcast"
	}
	if host == "open.spotify.com" && strings.HasPrefix(u.Path, "/episode") {
		return "podcast"
	}
	if strings.HasSuffix(host, ".libsyn.com") {
		return "podcast"
	}
	if strings.HasSuffix(strings.ToLower(u.Path), ".mp3") ||
		strings.HasSuffix(strings.ToLower(u.Path), ".m4a") {
		return "podcast"
	}

	return "article"
}
