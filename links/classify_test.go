package links_test

import (
	"testing"

	"darek/links"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://youtube.com/watch?v=abc", "video"},
		{"https://www.youtube.com/watch?v=abc", "video"},
		{"https://youtu.be/abc", "video"},
		{"https://vimeo.com/123", "video"},
		{"https://tiktok.com/@x/video/123", "video"},
		{"https://twitter.com/u/status/1", "tweet"},
		{"https://x.com/u/status/1", "tweet"},
		{"https://bsky.app/profile/u/post/1", "tweet"},
		{"https://anchor.fm/show/episodes/x", "podcast"},
		{"https://podcasts.apple.com/us/podcast/x/id1", "podcast"},
		{"https://open.spotify.com/episode/abc", "podcast"},
		{"https://overcast.fm/+abc", "podcast"},
		{"https://example.libsyn.com/episode-1", "podcast"},
		{"https://cdn.example.com/episode.mp3", "podcast"},
		{"https://cdn.example.com/episode.m4a", "podcast"},
		{"https://example.com/article", "article"},
		{"https://news.ycombinator.com/item?id=1", "article"},
		{"", "article"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := links.Classify(c.in); got != c.want {
				t.Errorf("Classify(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
