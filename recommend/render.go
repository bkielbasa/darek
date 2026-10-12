package recommend

import (
	"fmt"
	"html"
	"strings"
)

// RenderText renders suggestions as plain text for the digest. Empty input → "".
func RenderText(ss []Suggestion) string {
	if len(ss) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Reading suggestions\n")
	b.WriteString(strings.Repeat("─", 19))
	b.WriteString("\n\n")
	for i, s := range ss {
		fmt.Fprintf(&b, "▸ %s (%s)\n", displayTitle(s), s.Kind)
		if s.Reason != "" {
			fmt.Fprintf(&b, "   %s\n", s.Reason)
		}
		fmt.Fprintf(&b, "   %s\n", s.URL)
		if i < len(ss)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// RenderHTML renders suggestions as inline-styled HTML safe for email clients.
// Empty input → "".
func RenderHTML(ss []Suggestion) string {
	if len(ss) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<section class="recommend-digest" style="margin-top:1.5rem;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;color:#1c1c1c;">`)
	b.WriteString(`<h2 style="margin:0 0 .75rem;font-size:1.05rem;font-weight:600;">Reading suggestions</h2>`)
	for _, s := range ss {
		b.WriteString(`<div style="background:#fff;border:1px solid #e8e3d8;border-radius:6px;padding:.75rem 1rem;margin-bottom:.5rem;">`)
		fmt.Fprintf(&b, `<div style="margin-bottom:.35rem;"><a href="%s" style="color:#1a5fb4;text-decoration:none;font-weight:600;">%s</a> <span style="color:#6b6b6b;font-size:.9em;">· %s</span></div>`,
			html.EscapeString(s.URL), html.EscapeString(displayTitle(s)), html.EscapeString(s.Kind))
		if s.Reason != "" {
			fmt.Fprintf(&b, `<div style="line-height:1.45;color:#444;">%s</div>`, html.EscapeString(s.Reason))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section>`)
	return b.String()
}

func displayTitle(s Suggestion) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	return s.URL
}
