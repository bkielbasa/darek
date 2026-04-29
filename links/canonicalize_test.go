package links_test

import (
	"testing"

	"darek/links"
)

func TestCanonicalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"strips utm_source",
			"https://example.com/article?utm_source=twitter",
			"https://example.com/article"},
		{"strips multiple utm_*",
			"https://example.com/a?utm_source=tw&utm_medium=social&utm_campaign=launch",
			"https://example.com/a"},
		{"strips fbclid+gclid",
			"https://example.com/x?fbclid=abc&gclid=def",
			"https://example.com/x"},
		{"keeps real query params",
			"https://example.com/search?q=go&page=2",
			"https://example.com/search?page=2&q=go"},
		{"sorts query params alphabetically",
			"https://example.com/search?page=2&q=go",
			"https://example.com/search?page=2&q=go"},
		{"lowercases scheme + host",
			"HTTPS://Example.COM/Path",
			"https://example.com/Path"},
		{"strips www.",
			"https://www.example.com/x",
			"https://example.com/x"},
		{"drops trailing slash",
			"https://example.com/path/",
			"https://example.com/path"},
		{"keeps root slash",
			"https://example.com/",
			"https://example.com/"},
		{"drops fragment by default",
			"https://example.com/x#section",
			"https://example.com/x"},
		{"keeps fragment for twitter",
			"https://twitter.com/user/status/123#m",
			"https://twitter.com/user/status/123#m"},
		{"keeps youtube t param",
			"https://youtube.com/watch?v=abc&t=42",
			"https://youtube.com/watch?t=42&v=abc"},
		{"strips youtube si param",
			"https://youtu.be/abc?si=tracking",
			"https://youtu.be/abc"},
		{"empty input returns empty",
			"", ""},
		{"invalid url returns empty",
			"not a url at all",
			""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := links.Canonicalize(c.in)
			if got != c.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
