package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	followupHorizonDays = 14
	followupLimit       = 10
)

// FollowupView is what the digest renders. Only actionable items (dated
// agreements/events due within the horizon) belong here. Person facts are
// deliberately not rendered: they stay in the memories table for chat recall
// (memory.list) instead of being re-emailed every day.
type FollowupView struct {
	Items []Memory
}

// Recall fetches active dated follow-ups (overdue or due soon).
func Recall(ctx context.Context, store *Store) (FollowupView, error) {
	items, err := store.ActiveItems(ctx, followupHorizonDays, followupLimit)
	return FollowupView{Items: items}, err
}

// RenderText renders the follow-ups section. Empty view → "".
func RenderText(v FollowupView) string {
	if len(v.Items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Follow-ups\n")
	b.WriteString(strings.Repeat("─", 10))
	b.WriteString("\n\n")
	for _, m := range v.Items {
		fmt.Fprintf(&b, "▸ %s\n", followupLine(m))
	}
	return b.String()
}

func followupLine(m Memory) string {
	when := ""
	if m.DueAt != nil {
		d := m.DueAt.In(time.Local)
		if d.Before(time.Now()) {
			when = "overdue since " + d.Format("Mon 02 Jan") + " — "
		} else {
			when = d.Format("Mon 02 Jan 15:04") + " — "
		}
	}
	line := when + m.Summary
	if len(m.Groups) > 0 && m.Groups[0] != "" {
		line += " (" + m.Groups[0] + ")"
	}
	return line
}

// RenderHTML renders the follow-ups section as inline-styled email-safe HTML.
// Empty view → "".
func RenderHTML(v FollowupView) string {
	if len(v.Items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<section class="mem-digest" style="margin-top:1.5rem;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;color:#1c1c1c;">`)
	b.WriteString(`<h2 style="margin:0 0 .75rem;font-size:1.05rem;font-weight:600;">Follow-ups</h2>`)
	for _, m := range v.Items {
		b.WriteString(`<div style="background:#fff;border:1px solid #e8e3d8;border-radius:6px;padding:.6rem 1rem;margin-bottom:.4rem;">`)
		fmt.Fprintf(&b, `<div style="line-height:1.45;">%s</div>`, htmlEscape(followupLine(m)))
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section>`)
	return b.String()
}

func htmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
	).Replace(s)
}
