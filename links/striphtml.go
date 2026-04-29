package links

import (
	"html"
	"regexp"
	"strings"
)

var (
	tagRE         = regexp.MustCompile(`<[^>]*>`)
	wsRE          = regexp.MustCompile(`\s+`)
	wsBeforePunct = regexp.MustCompile(`\s+([.,;:!?])`)
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
	// Tag-replacement can leave a space before punctuation (e.g. "world ." from
	// "<b>world</b>."); strip those.
	out = wsBeforePunct.ReplaceAllString(out, "$1")
	return strings.TrimSpace(out)
}
