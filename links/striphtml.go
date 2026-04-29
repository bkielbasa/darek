package links

import (
	"html"
	"regexp"
	"strings"
)

var (
	tagRE = regexp.MustCompile(`<[^>]*>`)
	wsRE  = regexp.MustCompile(`\s+`)
)

// StripHTML returns the visible text content of an HTML fragment with
// whitespace collapsed. Entities are decoded. Suitable for short bodies like
// RSS summaries; not a full parser.
func StripHTML(s string) string {
	if s == "" {
		return ""
	}
	// Replace tags with a single space so adjacent words don't run together.
	out := tagRE.ReplaceAllString(s, " ")
	out = html.UnescapeString(out)
	out = wsRE.ReplaceAllString(out, " ")
	return strings.TrimSpace(out)
}
