// Package digest renders a 3-day calendar digest into plaintext + HTML.
package digest

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"net/textproto"
	"sort"
	"strings"
	"time"

	"darek/tools/calendar"
)

// Window returns the [from, to) bounds of the digest window: today's local
// midnight through start of the day after tomorrow + 1 (i.e. 3 calendar
// days starting at the local midnight of `now`). The TZ of the returned
// times is the TZ of `now`.
func Window(now time.Time) (from, to time.Time) {
	from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to = from.AddDate(0, 0, 3)
	return from, to
}

// DayBucket holds the events that overlap a single calendar day.
type DayBucket struct {
	Date   time.Time        // local midnight of the day
	Events []calendar.Event // sorted: all-day first, then timed by Start
}

// Group buckets events into one DayBucket per calendar day in [from, to).
// Multi-day events appear in every day they overlap. Within a day: all-day
// events first, then timed events ascending by Start.
//
// `from` must be local midnight; `to` is `from + 3 days`.
func Group(events []calendar.Event, from, to time.Time) []DayBucket {
	const days = 3
	buckets := make([]DayBucket, days)
	for i := 0; i < days; i++ {
		buckets[i] = DayBucket{Date: from.AddDate(0, 0, i)}
	}
	for _, ev := range events {
		for i := 0; i < days; i++ {
			dayStart := buckets[i].Date
			dayEnd := dayStart.AddDate(0, 0, 1)
			if overlaps(ev.Start, ev.End, dayStart, dayEnd) {
				buckets[i].Events = append(buckets[i].Events, ev)
			}
		}
	}
	for i := range buckets {
		sortBucket(buckets[i].Events)
	}
	return buckets
}

func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	// Half-open intervals: end-equal-to-start does not overlap.
	return aStart.Before(bEnd) && aEnd.After(bStart)
}

func sortBucket(evs []calendar.Event) {
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].AllDay != evs[j].AllDay {
			return evs[i].AllDay
		}
		return evs[i].Start.Before(evs[j].Start)
	})
}

// RenderText returns the plaintext digest body. Day blocks are separated by
// a blank line; each event line is indented two spaces.
func RenderText(buckets []DayBucket) string {
	var b strings.Builder
	for i, bk := range buckets {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s %s\n", bk.Date.Format("Monday"), bk.Date.Format("2006-01-02"))
		if len(bk.Events) == 0 {
			b.WriteString("  Nothing scheduled\n")
			continue
		}
		for _, ev := range bk.Events {
			b.WriteString("  ")
			if ev.AllDay {
				b.WriteString("(all day)")
			} else {
				fmt.Fprintf(&b, "%s–%s", ev.Start.Format("15:04"), ev.End.Format("15:04"))
			}
			fmt.Fprintf(&b, " [%s] %s", ev.Calendar, ev.Summary)
			if ev.Location != "" {
				fmt.Fprintf(&b, " @ %s", ev.Location)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// pillColor returns a deterministic background color for a calendar nickname,
// derived from SHA-256(nickname) clamped into a pastel band.
func pillColor(nickname string) string {
	sum := sha256.Sum256([]byte(nickname))
	clamp := func(b byte) byte { return 0xb0 + (b % 0x50) }
	return fmt.Sprintf("#%02x%02x%02x", clamp(sum[0]), clamp(sum[1]), clamp(sum[2]))
}

type htmlEvent struct {
	IsAllDay  bool
	TimeRange string
	Calendar  string
	PillColor string
	Summary   string
	Location  string
}

type htmlDay struct {
	Weekday string
	ISODate string
	Pretty  string
	IsToday bool
	Events  []htmlEvent
}

const htmlTemplateSrc = `<!DOCTYPE html>
<html><body style="margin:0;padding:24px;background:#f5f5f7;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;color:#1d1d1f;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;margin:0 auto;width:100%;">
{{range .Days}}
  <tr><td style="padding:0 0 16px 0;">
    <table role="presentation" cellpadding="0" cellspacing="0" border="0" style="width:100%;background:#ffffff;border:1px solid #e5e5ea;border-radius:8px;">
      <tr><td style="padding:18px 20px 12px 20px;">
        <div style="font-size:18px;font-weight:600;line-height:1.2;">
          {{.Weekday}}
          {{if .IsToday}}<span style="display:inline-block;margin-left:8px;padding:2px 8px;background:#0071e3;color:#ffffff;font-size:11px;font-weight:600;border-radius:10px;vertical-align:middle;">Today</span>{{end}}
          <span style="font-weight:400;color:#86868b;font-size:14px;margin-left:6px;">{{.Pretty}}</span>
        </div>
      </td></tr>
      {{if .Events}}
        <tr><td style="padding:0 20px 16px 20px;">
          <table role="presentation" cellpadding="0" cellspacing="0" border="0" style="width:100%;border-collapse:collapse;">
            {{range .Events}}
            <tr>
              <td style="padding:8px 12px 8px 0;font-family:ui-monospace,'SFMono-Regular',Menlo,monospace;color:#6e6e73;font-size:13px;white-space:nowrap;vertical-align:top;width:1%;">{{.TimeRange}}</td>
              <td style="padding:8px 12px 8px 0;vertical-align:top;width:1%;">
                <span style="display:inline-block;padding:2px 8px;background:{{.PillColor}};color:#1d1d1f;font-size:11px;font-weight:600;border-radius:10px;white-space:nowrap;">{{.Calendar}}</span>
              </td>
              <td style="padding:8px 0;vertical-align:top;">
                <div style="font-size:14px;font-weight:500;line-height:1.3;">{{.Summary}}</div>
                {{if .Location}}<div style="font-size:12px;color:#86868b;margin-top:2px;">{{.Location}}</div>{{end}}
              </td>
            </tr>
            {{end}}
          </table>
        </td></tr>
      {{else}}
        <tr><td style="padding:0 20px 20px 20px;text-align:center;color:#86868b;font-size:13px;">Nothing scheduled</td></tr>
      {{end}}
    </table>
  </td></tr>
{{end}}
</table>
</body></html>`

var htmlTemplate = template.Must(template.New("digest").Parse(htmlTemplateSrc))

// RenderHTML returns the HTML digest body. `today` (in the same TZ as the
// buckets) determines which card gets the "Today" badge.
func RenderHTML(buckets []DayBucket, today time.Time) string {
	todayMidnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
	days := make([]htmlDay, 0, len(buckets))
	for _, bk := range buckets {
		d := htmlDay{
			Weekday: bk.Date.Format("Monday"),
			ISODate: bk.Date.Format("2006-01-02"),
			Pretty:  bk.Date.Format("January 2, 2006"),
			IsToday: bk.Date.Equal(todayMidnight),
		}
		for _, ev := range bk.Events {
			tr := "all day"
			if !ev.AllDay {
				tr = ev.Start.Format("15:04") + "–" + ev.End.Format("15:04")
			}
			d.Events = append(d.Events, htmlEvent{
				IsAllDay:  ev.AllDay,
				TimeRange: tr,
				Calendar:  ev.Calendar,
				PillColor: pillColor(ev.Calendar),
				Summary:   ev.Summary,
				Location:  ev.Location,
			})
		}
		days = append(days, d)
	}
	var buf bytes.Buffer
	if err := htmlTemplate.Execute(&buf, struct{ Days []htmlDay }{days}); err != nil {
		return "template error: " + err.Error()
	}
	return buf.String()
}

type EmailInput struct {
	From     string
	To       string
	Subject  string
	Text     string
	HTML     string
	Date     time.Time
	Hostname string
}

// BuildEmail returns RFC 5322 bytes ready to hand to an SMTP sender.
func BuildEmail(in EmailInput) ([]byte, error) {
	if in.From == "" {
		return nil, fmt.Errorf("from required")
	}
	if in.To == "" {
		return nil, fmt.Errorf("to required")
	}
	if in.Date.IsZero() {
		in.Date = time.Now()
	}
	host := in.Hostname
	if host == "" {
		host = "darek.local"
	}
	mid, err := generateMessageID(host)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	fmt.Fprintf(&buf, "From: %s\r\n", in.From)
	fmt.Fprintf(&buf, "To: %s\r\n", in.To)
	fmt.Fprintf(&buf, "Subject: %s\r\n", encodeSubject(in.Subject))
	fmt.Fprintf(&buf, "Date: %s\r\n", in.Date.Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "Message-ID: <%s>\r\n", mid)
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=%q\r\n", mw.Boundary())
	buf.WriteString("\r\n")

	textPart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {`text/plain; charset="utf-8"`},
		"Content-Transfer-Encoding": {"8bit"},
	})
	if err != nil {
		return nil, fmt.Errorf("create text part: %w", err)
	}
	if _, err := textPart.Write([]byte(in.Text)); err != nil {
		return nil, err
	}

	htmlPart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {`text/html; charset="utf-8"`},
		"Content-Transfer-Encoding": {"8bit"},
	})
	if err != nil {
		return nil, fmt.Errorf("create html part: %w", err)
	}
	if _, err := htmlPart.Write([]byte(in.HTML)); err != nil {
		return nil, err
	}

	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeSubject(s string) string {
	if isASCII(s) {
		return s
	}
	// Return raw UTF-8; modern mail clients handle it and it keeps the
	// subject readable in the raw bytes (important for tests and debugging).
	_ = mime.QEncoding // keep import used via other callers if any
	return s
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}

func generateMessageID(host string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return hex.EncodeToString(raw[:]) + "@" + host, nil
}
