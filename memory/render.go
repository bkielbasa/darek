package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	followupHorizonDays    = 14
	followupRecentDays     = 7
	followupLimit          = 10
	personFactLookbackDays = 3
	personFactLimit        = 8
)

// FollowupView is what the digest renders.
type FollowupView struct {
	Items  []Memory
	People []Memory
}

// Recall fetches active follow-ups and recent person facts. Partial results are
// returned alongside a joined error.
func Recall(ctx context.Context, store *Store) (FollowupView, error) {
	items, ierr := store.ActiveItems(ctx, followupHorizonDays, followupRecentDays, followupLimit)
	people, perr := store.PersonFacts(ctx, personFactLookbackDays, personFactLimit)
	return FollowupView{Items: items, People: people}, errors.Join(ierr, perr)
}

// RenderText renders the follow-ups + people sections. Empty view → "".
func RenderText(v FollowupView) string {
	if len(v.Items) == 0 && len(v.People) == 0 {
		return ""
	}
	var b strings.Builder
	if len(v.Items) > 0 {
		b.WriteString("Follow-ups\n")
		b.WriteString(strings.Repeat("─", 10))
		b.WriteString("\n\n")
		for _, m := range v.Items {
			fmt.Fprintf(&b, "▸ %s\n", followupLine(m))
		}
	}
	if len(v.People) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("People\n")
		b.WriteString(strings.Repeat("─", 6))
		b.WriteString("\n\n")
		for _, m := range v.People {
			fmt.Fprintf(&b, "• %s\n", personLine(m))
		}
	}
	return b.String()
}

func followupLine(m Memory) string {
	when := "recent"
	if m.DueAt != nil {
		d := m.DueAt.In(time.Local)
		if d.Before(time.Now()) {
			when = "overdue since " + d.Format("Mon 02 Jan")
		} else {
			when = d.Format("Mon 02 Jan 15:04")
		}
	}
	line := fmt.Sprintf("%s — %s", when, m.Summary)
	if len(m.Groups) > 0 && m.Groups[0] != "" {
		line += " (" + m.Groups[0] + ")"
	}
	return line
}

func personLine(m Memory) string {
	if len(m.People) > 0 {
		return m.People[0] + " — " + m.Summary
	}
	return m.Summary
}

// RenderHTML renders the sections as inline-styled email-safe HTML. Empty view → "".
func RenderHTML(v FollowupView) string {
	if len(v.Items) == 0 && len(v.People) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<section class="mem-digest" style="margin-top:1.5rem;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;color:#1c1c1c;">`)
	if len(v.Items) > 0 {
		b.WriteString(`<h2 style="margin:0 0 .75rem;font-size:1.05rem;font-weight:600;">Follow-ups</h2>`)
		for _, m := range v.Items {
			b.WriteString(`<div style="background:#fff;border:1px solid #e8e3d8;border-radius:6px;padding:.6rem 1rem;margin-bottom:.4rem;">`)
			fmt.Fprintf(&b, `<div style="line-height:1.45;">%s</div>`, htmlEscape(followupLine(m)))
			b.WriteString(`</div>`)
		}
	}
	if len(v.People) > 0 {
		b.WriteString(`<h2 style="margin:1rem 0 .75rem;font-size:1.05rem;font-weight:600;">People</h2>`)
		for _, m := range v.People {
			fmt.Fprintf(&b, `<div style="line-height:1.5;">• %s</div>`, htmlEscape(personLine(m)))
		}
	}
	b.WriteString(`</section>`)
	return b.String()
}

func htmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
	).Replace(s)
}
