// Package blogfeed parses RSS 2.0 / Atom 1.0 feeds for the blog-marketing
// scheduler. It is intentionally minimal: only the fields needed downstream
// (URL, title, summary, published-at) are extracted, and only the dialects
// the user's blog actually emits are supported.
package blogfeed

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"time"

	"darek/links"
)

// Entry is a parsed feed item, post-canonicalization.
type Entry struct {
	URL          string    // exact URL from the feed
	CanonicalURL string    // result of links.Canonicalize(URL); used as dedupe key
	Title        string
	Summary      string
	PublishedAt  time.Time
}

// rssFeed and atomFeed are the minimal XML shapes we decode.
type rssFeed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			PubDate     string `xml:"pubDate"`
			DCDate      string `xml:"http://purl.org/dc/elements/1.1/ date"`
			Description string `xml:"description"`
		} `xml:"item"`
	} `xml:"channel"`
}

type atomFeed struct {
	XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	Entries []struct {
		Title string `xml:"title"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
		Published string `xml:"published"`
		Updated   string `xml:"updated"`
		Summary   string `xml:"summary"`
	} `xml:"entry"`
}

// Parse decodes an RSS 2.0 or Atom 1.0 feed body. The dialect is detected by
// the root element. Returned entries are sorted newest first by PublishedAt.
func Parse(body []byte) ([]Entry, error) {
	root, err := detectRoot(body)
	if err != nil {
		return nil, err
	}
	switch root {
	case "rss":
		return parseRSS(body)
	case "feed":
		return parseAtom(body)
	default:
		return nil, fmt.Errorf("blogfeed: unsupported feed root %q", root)
	}
}

// detectRoot reads the first start element from the XML stream.
func detectRoot(body []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("blogfeed: read root: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local, nil
		}
	}
}

func parseRSS(body []byte) ([]Entry, error) {
	var f rssFeed
	if err := xml.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("blogfeed: rss decode: %w", err)
	}
	out := make([]Entry, 0, len(f.Channel.Items))
	for _, it := range f.Channel.Items {
		raw := strings.TrimSpace(it.Link)
		if raw == "" {
			continue
		}
		published, err := parseRSSDate(it.PubDate, it.DCDate)
		if err != nil {
			return nil, fmt.Errorf("blogfeed: parse date for %q: %w", it.Title, err)
		}
		out = append(out, Entry{
			URL:          raw,
			CanonicalURL: links.Canonicalize(raw),
			Title:        strings.TrimSpace(it.Title),
			Summary:      strings.TrimSpace(it.Description),
			PublishedAt:  published,
		})
	}
	sortNewestFirst(out)
	return out, nil
}

func parseAtom(body []byte) ([]Entry, error) {
	var f atomFeed
	if err := xml.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("blogfeed: atom decode: %w", err)
	}
	out := make([]Entry, 0, len(f.Entries))
	for _, e := range f.Entries {
		href := pickAtomLink(e.Links)
		if href == "" {
			continue
		}
		raw := e.Published
		if raw == "" {
			raw = e.Updated
		}
		published, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, fmt.Errorf("blogfeed: parse atom date %q: %w", raw, err)
		}
		out = append(out, Entry{
			URL:          href,
			CanonicalURL: links.Canonicalize(href),
			Title:        strings.TrimSpace(e.Title),
			Summary:      strings.TrimSpace(e.Summary),
			PublishedAt:  published,
		})
	}
	sortNewestFirst(out)
	return out, nil
}

// pickAtomLink prefers rel="alternate" or unset rel; skips rel="self" / "edit".
func pickAtomLink(links []struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}) string {
	for _, l := range links {
		if l.Rel == "" || l.Rel == "alternate" {
			return l.Href
		}
	}
	return ""
}

// parseRSSDate accepts the common RSS pubDate formats plus a dc:date fallback.
func parseRSSDate(pubDate, dcDate string) (time.Time, error) {
	candidates := []string{}
	if pubDate != "" {
		candidates = append(candidates, pubDate)
	}
	if dcDate != "" {
		candidates = append(candidates, dcDate)
	}
	formats := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
	}
	for _, c := range candidates {
		for _, f := range formats {
			if t, err := time.Parse(f, c); err == nil {
				return t, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("no parseable date (pubDate=%q dc:date=%q)", pubDate, dcDate)
}

func sortNewestFirst(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].PublishedAt.After(entries[j].PublishedAt)
	})
}
