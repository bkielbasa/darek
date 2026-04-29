package links

import (
	"net/url"
	"sort"
	"strings"
)

// trackingParams is the closed set of query parameters stripped during
// canonicalization. Add hosts/params here to extend the rule.
var trackingParams = map[string]struct{}{
	"utm_source": {}, "utm_medium": {}, "utm_campaign": {}, "utm_term": {}, "utm_content": {},
	"fbclid": {}, "gclid": {}, "mc_eid": {}, "mc_cid": {}, "_hsenc": {}, "_hsmi": {},
	"ref": {}, "ref_src": {}, "ref_url": {}, "source": {}, "igshid": {},
	"share": {}, "share_id": {}, "feature": {},
}

// fragmentRoutingHosts is the allowlist of hosts whose URL fragments are
// load-bearing routing (SPA-style) and must be preserved.
var fragmentRoutingHosts = map[string]struct{}{
	"twitter.com": {}, "x.com": {}, "bsky.app": {},
}

// videoHostsStripSi lists hosts where the YouTube-style ?si= tracking param
// is stripped. Only video kinds — `si` is a real parameter on some non-video sites.
var videoHostsStripSi = map[string]struct{}{
	"youtube.com": {}, "youtu.be": {},
}

// Canonicalize normalizes a URL so that the same article reached via different
// referrers maps to the same key. Returns "" if the input is not parseable.
//
// Rules (applied in order):
//  1. Lowercase scheme and host. Drop "www." prefix.
//  2. Strip a closed allowlist of tracking query params (utm_*, fbclid, etc.).
//  3. On video hosts, also strip the ?si= param.
//  4. Sort remaining query params alphabetically.
//  5. Drop trailing slash unless path is exactly "/".
//  6. Drop fragments unless host is in fragmentRoutingHosts.
func Canonicalize(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Host = strings.TrimPrefix(u.Host, "www.")

	q := u.Query()
	for k := range q {
		if _, drop := trackingParams[k]; drop {
			q.Del(k)
		}
	}
	if _, isVideo := videoHostsStripSi[u.Host]; isVideo {
		q.Del("si")
	}
	if len(q) == 0 {
		u.RawQuery = ""
	} else {
		// Re-encode with keys sorted.
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for i, k := range keys {
			if i > 0 {
				b.WriteByte('&')
			}
			vs := q[k]
			sort.Strings(vs)
			for j, v := range vs {
				if j > 0 {
					b.WriteByte('&')
				}
				b.WriteString(url.QueryEscape(k))
				b.WriteByte('=')
				b.WriteString(url.QueryEscape(v))
			}
		}
		u.RawQuery = b.String()
	}

	if u.Path != "/" {
		u.Path = strings.TrimRight(u.Path, "/")
	}

	if _, keep := fragmentRoutingHosts[u.Host]; !keep {
		u.Fragment = ""
		u.RawFragment = ""
	}

	return u.String()
}
