package recommend

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// starColor matches the web UI's warm amber star color (--rating in
// cmd/darek/serve/static/style.css).
const starColor = "#d97757"

// RenderText renders suggestions as plain text for the digest. Empty input → "".
// When rateBase is non-empty, each suggestion gets 1-5 rating links of the form
// <rateBase>/r/<link-id>/<value> (served by darek serve's GET /r/{id}/{value}).
func RenderText(ss []Suggestion, rateBase string) string {
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
		if line := rateLine(s, rateBase); line != "" {
			fmt.Fprintf(&b, "   %s\n", line)
		}
		if i < len(ss)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// rateLine renders "Rate: 1 <url> · 2 <url> · ..." for the plain-text part.
func rateLine(s Suggestion, rateBase string) string {
	if rateBase == "" || s.ID == uuid.Nil {
		return ""
	}
	var parts []string
	for n := 1; n <= 5; n++ {
		parts = append(parts, fmt.Sprintf("%d %s", n, rateURL(rateBase, s.ID, n)))
	}
	return "Rate: " + strings.Join(parts, " · ")
}

// RenderHTML renders suggestions as inline-styled HTML safe for email clients.
// Empty input → "". See RenderText for the rateBase semantics.
func RenderHTML(ss []Suggestion, rateBase string) string {
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
		b.WriteString(rateStarsHTML(s, rateBase))
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section>`)
	return b.String()
}

// rateStarsHTML renders a "Rate: ★★★★★" row where each star links to its
// rating value. Stars are a plain set (no toggle) so re-clicks and email-link
// prefetching can only land on an explicit value.
func rateStarsHTML(s Suggestion, rateBase string) string {
	if rateBase == "" || s.ID == uuid.Nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div style="margin-top:.45rem;font-size:.9em;color:#6b6b6b;">Rate:`)
	for n := 1; n <= 5; n++ {
		fmt.Fprintf(&b, ` <a href="%s" style="color:%s;text-decoration:none;font-size:1.05em;" title="rate %d">★</a>`,
			html.EscapeString(rateURL(rateBase, s.ID, n)), starColor, n)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func rateURL(base string, id uuid.UUID, value int) string {
	return strings.TrimRight(base, "/") + "/r/" + id.String() + "/" + strconv.Itoa(value)
}

func displayTitle(s Suggestion) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	return s.URL
}
