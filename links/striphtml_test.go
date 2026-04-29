package links_test

import (
	"testing"

	"darek/links"
)

func TestStripHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text passes through", "hello world", "hello world"},
		{"single tag dropped", "<p>hello</p>", "hello"},
		{"nested tags dropped", "<div><p>hello <b>world</b></p></div>", "hello world"},
		{"entities decoded", "AT&amp;T &lt;3 &quot;tea&quot;", `AT&T <3 "tea"`},
		{"line breaks become spaces", "<p>one</p><p>two</p>", "one two"},
		{"collapses whitespace runs", "  hello   world\n\n\nfoo  ", "hello world foo"},
		{"empty input returns empty", "", ""},
		{"only tags returns empty", "<br><br/>", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := links.StripHTML(c.in)
			if got != c.want {
				t.Errorf("StripHTML(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
